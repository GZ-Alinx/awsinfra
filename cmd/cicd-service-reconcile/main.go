package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/gitlab"
	"ops-deploy-platform/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	serviceFile := flag.String("service-file", "", "JSON file containing services to merge")
	dockerfilesRoot := flag.String("dockerfiles-root", "", "directory containing <service>/Dockerfile")
	imagePrefix := flag.String("image-prefix", "", "ECR URI prefix applied to every service")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*dockerfilesRoot) == "" {
		fatal(fmt.Errorf("project and dockerfiles-root are required"))
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
	byKey := make(map[string]gitlab.ServiceSpec, len(delivery.Services))
	for _, item := range delivery.Services {
		byKey[item.Key] = item
	}
	if strings.TrimSpace(*serviceFile) != "" {
		data, readErr := os.ReadFile(*serviceFile)
		if readErr != nil {
			fatal(readErr)
		}
		var additions []gitlab.ServiceSpec
		if err := json.Unmarshal(data, &additions); err != nil {
			fatal(fmt.Errorf("decode service file: %w", err))
		}
		for _, item := range additions {
			if _, exists := byKey[item.Key]; exists {
				fatal(fmt.Errorf("service %s already exists; additions must be new", item.Key))
			}
			byKey[item.Key] = item
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	delivery.Services = make([]gitlab.ServiceSpec, 0, len(keys))
	for _, key := range keys {
		item := byKey[key]
		content, readErr := os.ReadFile(filepath.Join(*dockerfilesRoot, key, "Dockerfile"))
		if readErr != nil {
			fatal(fmt.Errorf("read Dockerfile for %s: %w", key, readErr))
		}
		item.DockerfileContent = string(content)
		item.ManifestMode = "repository"
		if prefix := strings.TrimSuffix(strings.TrimSpace(*imagePrefix), "/"); prefix != "" {
			item.ImageRepository = prefix + "/" + key
		}
		delivery.Services = append(delivery.Services, item)
	}
	saved, err := service.SaveDelivery(ctx, *project, delivery)
	if err != nil {
		fatal(err)
	}
	result := make([]map[string]string, 0, len(saved.Services))
	for _, item := range saved.Services {
		result = append(result, map[string]string{"key": item.Key, "source": item.SourceRepository, "branch": item.SourceBranch, "image": item.ImageRepository, "manifest_mode": item.ManifestMode})
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
