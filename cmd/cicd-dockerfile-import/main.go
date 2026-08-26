package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/gitlab"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	root := flag.String("root", "", "directory containing <service>/Dockerfile")
	repositoryManifests := flag.Bool("repository-manifests", false, "mark deployment manifests as repository maintained")
	flag.Parse()
	if *project == "" || *root == "" {
		fatal(fmt.Errorf("project and root are required"))
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
	delivery, err := service.GetDelivery(ctx, *project)
	if err != nil {
		fatal(err)
	}
	updated := 0
	for index := range delivery.Services {
		path := filepath.Join(*root, delivery.Services[index].Key, "Dockerfile")
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			fatal(fmt.Errorf("read %s: %w", path, readErr))
		}
		delivery.Services[index].Dockerfile = "dockerfiles/" + delivery.Services[index].Key + "/Dockerfile"
		delivery.Services[index].DockerfileContent = string(content)
		if *repositoryManifests {
			delivery.Services[index].ManifestMode = "repository"
		}
		clear(content)
		updated++
	}
	if _, err := service.SaveDelivery(ctx, *project, delivery); err != nil {
		fatal(err)
	}
	fmt.Printf("project=%s dockerfiles=%d repository_manifests=%t\n", *project, updated, *repositoryManifests)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
