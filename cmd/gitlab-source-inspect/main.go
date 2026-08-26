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
	search := flag.String("search", "", "source repository search")
	projectID := flag.Int64("repository-id", 0, "optional source repository project ID; lists branches")
	flag.Parse()
	if *project == "" {
		fatal(fmt.Errorf("project is required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	var result any
	if *projectID > 0 {
		result, err = service.ListSourceRepositoryBranches(ctx, *project, *projectID)
	} else {
		result, err = service.ListSourceRepositories(ctx, *project, *search)
	}
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
