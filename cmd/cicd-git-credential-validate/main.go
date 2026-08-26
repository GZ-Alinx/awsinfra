package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/cicd"
	"ops-deploy-platform/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	credential := flag.String("credential", "", "platform credential key")
	repository := flag.String("repository", "", "Git HTTPS repository URL")
	flag.Parse()
	if *project == "" || *credential == "" || *repository == "" {
		fatal(fmt.Errorf("project, credential and repository are required"))
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
	service, err := cicd.New(config, store)
	if err != nil {
		fatal(err)
	}
	result, err := service.ValidateGitCredential(ctx, *project, *credential, *repository)
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
	if !result.SmartHTTP {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
