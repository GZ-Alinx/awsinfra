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
	"github.com/GZ-Alinx/awsinfra/internal/gitlab"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	key := flag.String("key", "", "GitLab server key")
	name := flag.String("name", "", "GitLab server display name")
	baseURL := flag.String("url", "", "GitLab base URL")
	rootGroup := flag.String("root-group", "", "GitLab root group")
	project := flag.String("project", "", "optional project to bind as source GitLab")
	defaultBranch := flag.String("default-branch", "main", "default branch")
	visibility := flag.String("visibility", "private", "repository visibility")
	allowHTTP := flag.Bool("allow-insecure-http", false, "allow an HTTP GitLab URL")
	testOnly := flag.Bool("test-only", false, "only retest an existing server without reading a token")
	flag.Parse()
	if strings.TrimSpace(*key) == "" || (!*testOnly && (strings.TrimSpace(*name) == "" || strings.TrimSpace(*baseURL) == "" || strings.TrimSpace(*rootGroup) == "")) {
		fatal(fmt.Errorf("key, name, url and root-group are required"))
	}

	token := ""
	if !*testOnly {
		tokenBytes, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if err != nil {
			fatal(err)
		}
		token = strings.TrimSpace(string(tokenBytes))
		clear(tokenBytes)
		if token == "" || len(token) > 4096 {
			fatal(fmt.Errorf("a valid access token must be provided on stdin"))
		}
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
	service, err := gitlab.New(config, store)
	if err != nil {
		fatal(err)
	}
	var server gitlab.Server
	if !*testOnly {
		server, err = service.SaveServer(ctx, *key, gitlab.ServerInput{
			Key:               *key,
			DisplayName:       *name,
			BaseURL:           *baseURL,
			AccessToken:       token,
			RootGroup:         *rootGroup,
			DefaultBranch:     *defaultBranch,
			Visibility:        *visibility,
			AllowInsecureHTTP: *allowHTTP,
		})
		token = ""
		if err != nil {
			fatal(err)
		}
	}
	server, err = service.TestServer(ctx, *key)
	if err != nil {
		fatal(err)
	}

	result := map[string]any{"server": server, "connection_test": "healthy"}
	if strings.TrimSpace(*project) != "" {
		delivery, err := service.GetDelivery(ctx, *project)
		if err != nil {
			fatal(err)
		}
		delivery.SourceServerKey = server.Key
		delivery, err = service.SaveDelivery(ctx, *project, delivery)
		if err != nil {
			fatal(err)
		}
		result["project"] = delivery.ProjectKey
		result["source_server_key"] = delivery.SourceServerKey
		result["registered_services"] = len(delivery.Services)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
