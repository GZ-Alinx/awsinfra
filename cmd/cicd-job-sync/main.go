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
	"github.com/GZ-Alinx/awsinfra/internal/gitlab"
	"github.com/GZ-Alinx/awsinfra/internal/kubetunnel"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	jobKey := flag.String("job", "", "CI/CD job key")
	flag.Parse()
	if *project == "" || *jobKey == "" {
		fatal(fmt.Errorf("project and job are required"))
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
	cicdService, err := cicd.New(config, store)
	if err != nil {
		fatal(err)
	}
	awsService, err := awscredentials.New(config, store)
	if err != nil {
		fatal(err)
	}
	tunnels := kubetunnel.New(config, awsService)
	defer tunnels.Close()
	cicdService.SetTunnelProvider(tunnels)
	gitlabService, err := gitlab.New(config, store)
	if err != nil {
		fatal(err)
	}
	job, err := cicdService.PrepareGeneratedJobRepositories(ctx, *project, *jobKey)
	if err != nil {
		fatal(err)
	}
	if job.JenkinsfileMode == "generated" {
		if _, err := gitlabService.Provision(ctx, *project); err != nil {
			fatal(err)
		}
		if _, _, err := gitlabService.SyncJobJenkinsfile(ctx, *project, job); err != nil {
			fatal(err)
		}
	}
	job, err = cicdService.SyncJob(ctx, *project, *jobKey)
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(job); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
