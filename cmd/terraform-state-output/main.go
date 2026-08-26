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
	"ops-deploy-platform/internal/persistence"
	"ops-deploy-platform/internal/statebackend"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	target := flag.String("target", "", "environment or shared target")
	stage := flag.String("stage", "infra", "Terraform stage")
	keys := flag.String("keys", "", "comma-separated non-sensitive output keys")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*target) == "" || strings.TrimSpace(*keys) == "" {
		fatal(fmt.Errorf("project, target and keys are required"))
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
	service, err := statebackend.New(config, store)
	if err != nil {
		fatal(err)
	}
	outputs, err := service.StateOutputs(ctx, *project, *target, *stage)
	if err != nil {
		fatal(err)
	}
	result := map[string]any{}
	for _, raw := range strings.Split(*keys, ",") {
		key := strings.TrimSpace(raw)
		lower := strings.ToLower(key)
		if key == "" || strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "secret_value") {
			fatal(fmt.Errorf("sensitive or empty output key is not allowed: %q", key))
		}
		result[key] = outputs[key]
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
