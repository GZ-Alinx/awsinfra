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
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	environment := flag.String("environment", "", "environment key")
	limit := flag.Int("limit", 20, "maximum builds")
	buildID := flag.String("build", "", "optional build ID; prints its logs")
	offset := flag.Int64("offset", 0, "optional Jenkins progressive log byte offset")
	cancelBuild := flag.Bool("cancel", false, "cancel the selected build")
	flag.Parse()
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
	awsService, err := awscredentials.New(config, store)
	if err != nil {
		fatal(err)
	}
	tunnels := kubetunnel.New(config, awsService)
	defer tunnels.Close()
	service.SetTunnelProvider(tunnels)
	if *buildID != "" {
		if *cancelBuild {
			build, cancelErr := service.CancelBuild(ctx, *project, *buildID)
			if cancelErr != nil {
				fatal(cancelErr)
			}
			if err := json.NewEncoder(os.Stdout).Encode(build); err != nil {
				fatal(err)
			}
			return
		}
		logs, logErr := service.BuildLogs(ctx, *project, *buildID, *offset)
		if logErr != nil {
			fatal(logErr)
		}
		fmt.Print(logs.Text)
		return
	}
	items, err := service.ListBuilds(ctx, *project, *environment, *limit)
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(items); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
