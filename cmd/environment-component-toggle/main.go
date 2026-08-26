package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/access"
	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	environmentKey := flag.String("environment", "", "environment key")
	component := flag.String("component", "", "component catalog key")
	enabled := flag.Bool("enabled", true, "desired component state")
	useDefaultSource := flag.Bool("use-default-source", false, "restore the platform-managed chart source")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*environmentKey) == "" || strings.TrimSpace(*component) == "" {
		fatal(fmt.Errorf("project, environment and component are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	accessService := access.NewService(store)
	target, err := accessService.Environment(ctx, *project, *environmentKey)
	if err != nil {
		fatal(err)
	}
	repository, err := environment.NewRepositoryWithStore(config.Paths.EnvironmentsDir, store)
	if err != nil {
		fatal(err)
	}
	document, err := repository.Load(target.TargetName)
	if err != nil {
		fatal(err)
	}
	components, ok := document["components"].(map[string]any)
	if !ok {
		fatal(fmt.Errorf("environment components configuration is missing"))
	}
	catalog, ok := components["catalog"].(map[string]any)
	if !ok {
		fatal(fmt.Errorf("environment component catalog is missing"))
	}
	entry, ok := catalog[strings.ToLower(strings.TrimSpace(*component))].(map[string]any)
	if !ok {
		fatal(fmt.Errorf("component is not registered in this environment"))
	}
	entry["enabled"] = *enabled
	if *useDefaultSource {
		defaults := environment.DefaultDocument(target.ProjectKey, target.Environment)
		defaultComponents, _ := defaults["components"].(map[string]any)
		defaultCatalog, _ := defaultComponents["catalog"].(map[string]any)
		defaultEntry, ok := defaultCatalog[strings.ToLower(strings.TrimSpace(*component))].(map[string]any)
		if !ok {
			fatal(fmt.Errorf("component has no platform-managed default source"))
		}
		for _, key := range []string{"repository", "chart", "chart_version", "builtin_chart"} {
			if value, exists := defaultEntry[key]; exists {
				entry[key] = value
			} else {
				delete(entry, key)
			}
		}
	}
	if err := repository.Save(target.TargetName, document); err != nil {
		fatal(err)
	}
	fmt.Printf("project=%s environment=%s component=%s enabled=%t\n", target.ProjectKey, target.Environment, *component, *enabled)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
