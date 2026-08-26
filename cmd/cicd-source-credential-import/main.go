package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/awscredentials"
	"github.com/GZ-Alinx/awsinfra/internal/cicd"
	"github.com/GZ-Alinx/awsinfra/internal/gitlab"
	"github.com/GZ-Alinx/awsinfra/internal/kubetunnel"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

type deployToken struct {
	ID       int64    `json:"id"`
	Username string   `json:"username"`
	Token    string   `json:"token"`
	Scopes   []string `json:"scopes"`
}

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	connection := flag.String("connection", "", "Jenkins connection key")
	services := flag.String("services", "", "comma-separated service keys")
	flag.Parse()
	projectKey, connectionKey := strings.ToLower(strings.TrimSpace(*project)), strings.ToLower(strings.TrimSpace(*connection))
	serviceKeys := split(*services)
	if projectKey == "" || connectionKey == "" || len(serviceKeys) == 0 {
		fatal(fmt.Errorf("project, connection and services are required"))
	}
	var token deployToken
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 16<<10)).Decode(&token); err != nil {
		fatal(fmt.Errorf("decode deploy token: %w", err))
	}
	defer func() { token.Token = "" }()
	if token.ID < 1 || strings.TrimSpace(token.Username) == "" || strings.TrimSpace(token.Token) == "" || !contains(token.Scopes, "read_repository") {
		fatal(fmt.Errorf("a read_repository deploy token is required"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	config, err := appconfig.Load(*configPath)
	if err != nil {
		fatal(err)
	}
	store, err := persistence.Open(ctx, config)
	if err != nil {
		fatal(err)
	}
	defer store.Close()
	cicdService, err := cicd.New(config, store)
	if err != nil {
		fatal(err)
	}
	awsService, err := awscredentials.New(config, store)
	if err != nil {
		fatal(err)
	}
	tunnels := kubetunnel.New(config, awsService)
	defer tunnels.Close()
	cicdService.SetTunnelProvider(tunnels)
	credentialKey := "gitlab-source-read-" + connectionKey
	if len(credentialKey) > 63 {
		fatal(fmt.Errorf("connection key is too long for the source credential"))
	}
	credential, err := cicdService.SaveCredential(ctx, projectKey, credentialKey, cicd.CredentialInput{
		ConnectionKey: connectionKey,
		DisplayName:   projectKey + " 业务源码只读凭据",
		Kind:          "gitlab_token",
		ExternalID:    "ops-" + projectKey + "-source-read",
		Description:   "业务 GitLab 源码只读凭据（兼容模式）；当 Deploy Token API 可用时由平台自动轮换为最小权限凭据",
		Username:      strings.TrimSpace(token.Username),
		Password:      strings.TrimSpace(token.Token),
	})
	token.Token = ""
	if err != nil {
		fatal(err)
	}
	credential, err = cicdService.SyncCredential(ctx, projectKey, credential.Key)
	if err != nil {
		fatal(err)
	}
	gitlabService, err := gitlab.New(config, store)
	if err != nil {
		fatal(err)
	}
	delivery, err := gitlabService.GetDelivery(ctx, projectKey)
	if err != nil {
		fatal(err)
	}
	serviceByKey := make(map[string]gitlab.ServiceSpec, len(delivery.Services))
	for _, service := range delivery.Services {
		serviceByKey[service.Key] = service
	}
	for _, serviceKey := range serviceKeys {
		service, ok := serviceByKey[serviceKey]
		if !ok {
			fatal(fmt.Errorf("service %s is not registered", serviceKey))
		}
		inspection, err := cicdService.ValidateGitCredential(ctx, projectKey, credential.Key, service.SourceRepository)
		if err != nil || !inspection.SmartHTTP {
			fatal(fmt.Errorf("read_repository token cannot clone service %s", serviceKey))
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(credential)
}

func split(value string) []string {
	items := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
