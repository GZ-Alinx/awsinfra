package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/gitlab"
	"ops-deploy-platform/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	baseURL := flag.String("base-url", "", "alternate GitLab HTTPS root URL")
	flag.Parse()
	if *project == "" || *baseURL == "" {
		fatal(fmt.Errorf("project and base-url are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
	result, err := service.CheckServerAlias(ctx, *project, *baseURL)
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
	if !result.MatchesCurrentProjects {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
