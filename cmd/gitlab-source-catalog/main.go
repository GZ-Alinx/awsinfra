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

type repositoryBranches struct {
	Repository gitlab.SourceRepositoryOption `json:"repository"`
	Branches   []gitlab.SourceBranchOption   `json:"branches"`
}

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	flag.Parse()
	if *project == "" {
		fatal(fmt.Errorf("project is required"))
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
	repositories, err := service.ListSourceRepositories(ctx, *project, "")
	if err != nil {
		fatal(err)
	}
	result := make([]repositoryBranches, 0, len(repositories))
	for _, repository := range repositories {
		branches, err := service.ListSourceRepositoryBranches(ctx, *project, repository.ProjectID)
		if err != nil {
			fatal(fmt.Errorf("list %s branches: %w", repository.Path, err))
		}
		result = append(result, repositoryBranches{Repository: repository, Branches: branches})
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
