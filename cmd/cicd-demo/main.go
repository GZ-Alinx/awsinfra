package main

import (
	"context"
	"encoding/json"
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

type result struct {
	BuildID       string            `json:"build_id"`
	Project       string            `json:"project"`
	Environment   string            `json:"environment"`
	Status        string            `json:"status"`
	Progress      int               `json:"progress"`
	CurrentStage  string            `json:"current_stage,omitempty"`
	BuildNumber   int64             `json:"build_number,omitempty"`
	Stages        []cicd.BuildStage `json:"stages,omitempty"`
	LogLines      int               `json:"log_lines,omitempty"`
	DeploymentLog string            `json:"deployment_log,omitempty"`
}

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
	environment := flag.String("environment", "", "environment key; defaults to the connection environment")
	requestedBy := flag.String("requested-by", "platform-demo", "audit identity")
	wait := flag.Bool("wait", true, "wait for the demo build to finish")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*connection) == "" {
		return fmt.Errorf("project and connection are required")
	}
	config, err := appconfig.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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

	build, err := service.StartDemoBuild(ctx, *project, *connection, *environment, *requestedBy)
	if err != nil {
		return err
	}
	if *wait {
		build, err = waitForBuild(ctx, service, *project, build)
		if err != nil {
			return err
		}
	}
	output := result{BuildID: build.ID, Project: build.ProjectKey, Environment: build.Environment, Status: build.Status, Progress: build.Progress, CurrentStage: build.CurrentStage, BuildNumber: build.BuildNumber, Stages: build.Stages}
	if build.BuildNumber > 0 {
		if logs, logErr := service.BuildLogs(ctx, *project, build.ID, 0); logErr == nil {
			output.LogLines = len(strings.Split(strings.TrimSpace(logs.Text), "\n"))
		}
		if deployment, deploymentErr := service.BuildDeploymentLogs(ctx, *project, build.ID); deploymentErr == nil {
			output.DeploymentLog = strings.TrimSpace(deployment.Text)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func waitForBuild(ctx context.Context, service *cicd.Service, project string, target cicd.Build) (cicd.Build, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		builds, err := service.ListBuilds(ctx, project, target.Environment, 100)
		if err != nil {
			return target, err
		}
		for _, build := range builds {
			if build.ID != target.ID {
				continue
			}
			target = build
			if build.Status != "queued" && build.Status != "running" {
				return build, nil
			}
			break
		}
		select {
		case <-ctx.Done():
			return target, ctx.Err()
		case <-ticker.C:
		}
	}
}
