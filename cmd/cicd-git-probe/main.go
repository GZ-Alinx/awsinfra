package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
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
	connection := flag.String("connection", "", "Jenkins connection key")
	environment := flag.String("environment", "", "environment key")
	credential := flag.String("credential", "", "platform credential key")
	repository := flag.String("repository", "", "Git repository URL")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
	build, err := service.StartGitCredentialProbe(ctx, *project, *connection, *environment, *credential, *repository)
	if err != nil {
		fatal(err)
	}
	for build.Status == "queued" || build.Status == "running" {
		time.Sleep(2 * time.Second)
		items, listErr := service.ListBuilds(ctx, *project, *environment, 100)
		if listErr != nil {
			fatal(listErr)
		}
		for _, item := range items {
			if item.ID == build.ID {
				build = item
				break
			}
		}
	}
	logs, _ := service.BuildLogs(ctx, *project, build.ID, 0)
	fmt.Printf("build=%s status=%s number=%d\n%s\n", build.ID, build.Status, build.BuildNumber, tail(logs.Text, 60))
	if build.Status != "succeeded" {
		os.Exit(1)
	}
}

func tail(value string, lines int) string {
	items := strings.Split(strings.TrimSpace(value), "\n")
	if len(items) > lines {
		items = items[len(items)-lines:]
	}
	return strings.Join(items, "\n")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
