package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/awscredentials"
	"ops-deploy-platform/internal/cicd"
	"ops-deploy-platform/internal/gitlab"
	"ops-deploy-platform/internal/kubetunnel"
	"ops-deploy-platform/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	connection := flag.String("connection", "", "Jenkins connection key")
	services := flag.String("services", "", "comma-separated service keys")
	flag.Parse()
	serviceKeys := make([]string, 0)
	for _, key := range strings.Split(*services, ",") {
		if key = strings.TrimSpace(key); key != "" {
			serviceKeys = append(serviceKeys, key)
		}
	}
	if *project == "" || *connection == "" || len(serviceKeys) == 0 {
		fatal(fmt.Errorf("project, connection and services are required"))
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
	gitlabService, err := gitlab.New(config, store)
	if err != nil {
		fatal(err)
	}
	credential, err := gitlabService.SyncSourceCredential(ctx, *project, *connection, serviceKeys, cicdService)
	if err != nil {
		fatal(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(credential)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
