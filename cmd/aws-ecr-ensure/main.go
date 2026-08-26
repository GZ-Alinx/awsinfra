package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/awscatalog"
	"github.com/GZ-Alinx/awsinfra/internal/awscredentials"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	region := flag.String("region", "", "AWS region")
	repository := flag.String("repository", "", "ECR repository name or URI")
	flag.Parse()
	if *project == "" || *region == "" || *repository == "" {
		fatal(fmt.Errorf("project, region and repository are required"))
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
	credentials, err := awscredentials.New(config, store)
	if err != nil {
		fatal(err)
	}
	item, err := awscatalog.New(config, credentials).EnsureECRRepository(ctx, *project, *region, *repository)
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(item); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
