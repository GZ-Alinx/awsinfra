package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/access"
	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/awscredentials"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
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
	connectionKey := flag.String("connection", "", "managed Jenkins connection key used to locate the EKS cluster")
	environmentKey := flag.String("environment", "", "project environment key used to locate the configured EKS cluster")
	tool := flag.String("tool", "kubectl", "cluster client: kubectl or helm")
	timeout := flag.Duration("timeout", 2*time.Minute, "command timeout")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || flag.NArg() == 0 {
		return fmt.Errorf("project and kubectl arguments are required")
	}
	if strings.TrimSpace(*connectionKey) == "" && strings.TrimSpace(*environmentKey) == "" {
		return fmt.Errorf("connection or environment is required to locate the EKS cluster")
	}
	if strings.TrimSpace(*connectionKey) != "" && strings.TrimSpace(*environmentKey) != "" {
		return fmt.Errorf("connection and environment are mutually exclusive")
	}
	client := strings.ToLower(strings.TrimSpace(*tool))
	if client != "kubectl" && client != "helm" {
		return fmt.Errorf("tool must be kubectl or helm")
	}
	config, err := appconfig.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	store, err := persistence.Open(ctx, config)
	if err != nil {
		return err
	}
	defer store.Close()
	region, clusterName, err := resolveCluster(ctx, config, store, strings.TrimSpace(*project), strings.TrimSpace(*connectionKey), strings.TrimSpace(*environmentKey))
	if err != nil {
		return err
	}
	awsService, err := awscredentials.New(config, store)
	if err != nil {
		return err
	}
	awsEnv, err := awsService.Environment(ctx, *project)
	if err != nil {
		return err
	}
	runtimeEnv := append(withoutAWS(os.Environ()), awsEnv...)
	runtimeEnv = append(runtimeEnv, "AWS_REGION="+region, "AWS_DEFAULT_REGION="+region)
	tempDir, err := os.MkdirTemp("", "ops-project-kubeconfig-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	kubeconfig := filepath.Join(tempDir, "config")
	update := exec.CommandContext(ctx, config.Tools.AWS, "eks", "update-kubeconfig", "--name", clusterName, "--region", region, "--kubeconfig", kubeconfig, "--alias", "ops-project-target", "--no-cli-pager") // #nosec G204 -- all values come from validated platform records.
	update.Env = runtimeEnv
	if output, err := update.CombinedOutput(); err != nil {
		return fmt.Errorf("prepare project kubeconfig: %w: %s", err, strings.TrimSpace(string(output)))
	}
	toolName := config.Tools.Kubectl
	args := append([]string{"--kubeconfig", kubeconfig, "--context", "ops-project-target"}, flag.Args()...)
	if client == "helm" {
		toolName = config.Tools.Helm
		args = append([]string(nil), flag.Args()...)
		runtimeEnv = append(runtimeEnv, "KUBECONFIG="+kubeconfig)
	}
	command := exec.CommandContext(ctx, toolName, args...) // #nosec G204 -- this is an explicit administrator CLI.
	command.Env = runtimeEnv
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", client, err)
	}
	return nil
}

func resolveCluster(ctx context.Context, config *appconfig.Config, store *persistence.Services, project, connectionKey, environmentKey string) (string, string, error) {
	if connectionKey != "" {
		connection, err := store.GetCICDConnection(ctx, project, strings.ToLower(connectionKey))
		if err != nil {
			return "", "", err
		}
		if connection.ClusterName == "" || connection.Region == "" || connection.ConnectionMode != "eks_port_forward" {
			return "", "", fmt.Errorf("connection is not bound to a managed EKS cluster")
		}
		return connection.Region, connection.ClusterName, nil
	}
	accessService := access.NewService(store)
	target, err := accessService.Environment(ctx, project, strings.ToLower(environmentKey))
	if err != nil {
		return "", "", err
	}
	repository, err := environment.NewRepositoryWithStore(config.Paths.EnvironmentsDir, store)
	if err != nil {
		return "", "", err
	}
	document, err := repository.Load(target.TargetName)
	if err != nil {
		return "", "", err
	}
	region := strings.TrimSpace(fmt.Sprint(document["region"]))
	clusterName := environment.ClusterName(document)
	if region == "" || clusterName == "" {
		return "", "", fmt.Errorf("environment %s/%s is not bound to an EKS cluster", project, environmentKey)
	}
	return region, clusterName, nil
}

func withoutAWS(source []string) []string {
	blocked := map[string]bool{"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AWS_PROFILE": true, "AWS_DEFAULT_PROFILE": true, "AWS_REGION": true, "AWS_DEFAULT_REGION": true}
	result := make([]string, 0, len(source))
	for _, item := range source {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			result = append(result, item)
		}
	}
	return result
}
