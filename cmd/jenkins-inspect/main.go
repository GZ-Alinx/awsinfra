package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/awscredentials"
	"github.com/GZ-Alinx/awsinfra/internal/cicd"
	"github.com/GZ-Alinx/awsinfra/internal/kubetunnel"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	connection := flag.String("connection", "", "Jenkins connection key")
	flag.Parse()
	if *project == "" || *connection == "" {
		return fmt.Errorf("project and connection are required")
	}
	config, err := appconfig.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := persistence.Open(ctx, config)
	if err != nil {
		return err
	}
	defer store.Close()
	awsProvider, err := awscredentials.New(config, store)
	if err != nil {
		return err
	}
	service, err := cicd.New(config, store)
	if err != nil {
		return err
	}
	tunnels := kubetunnel.New(config, awsProvider)
	defer tunnels.Close()
	service.SetTunnelProvider(tunnels)
	items, err := service.InspectJenkinsJobs(ctx, *project, *connection)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(items)
}
