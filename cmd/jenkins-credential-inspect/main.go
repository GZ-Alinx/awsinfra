package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/awscredentials"
	"ops-deploy-platform/internal/cicd"
	"ops-deploy-platform/internal/kubetunnel"
	"ops-deploy-platform/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	connection := flag.String("connection", "", "Jenkins connection key")
	credential := flag.String("credential", "", "platform credential key")
	flag.Parse()
	if *project == "" || *connection == "" || *credential == "" {
		fmt.Fprintln(os.Stderr, "project, connection and credential are required")
		os.Exit(2)
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
	awsService, err := awscredentials.New(config, store)
	if err != nil {
		fatal(err)
	}
	service, err := cicd.New(config, store)
	if err != nil {
		fatal(err)
	}
	tunnels := kubetunnel.New(config, awsService)
	defer tunnels.Close()
	service.SetTunnelProvider(tunnels)
	inspection, err := service.InspectCredential(ctx, *project, *credential)
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(inspection); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
