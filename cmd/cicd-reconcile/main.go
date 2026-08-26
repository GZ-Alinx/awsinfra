package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/awscredentials"
	"ops-deploy-platform/internal/cicd"
	"ops-deploy-platform/internal/gitlab"
	"ops-deploy-platform/internal/kubetunnel"
	"ops-deploy-platform/internal/persistence"
)

type reconcileInput struct {
	Project         string               `json:"project"`
	ConnectionKey   string               `json:"connection_key"`
	Services        []gitlab.ServiceSpec `json:"services"`
	Jobs            []jobInput           `json:"jobs"`
	DockerfilesRoot string               `json:"dockerfiles_root,omitempty"`
}

type jobInput struct {
	Key               string            `json:"key"`
	DisplayName       string            `json:"display_name"`
	JenkinsJobName    string            `json:"jenkins_job_name"`
	Jenkinsfile       string            `json:"jenkinsfile"`
	JenkinsfileMode   string            `json:"jenkinsfile_mode"`
	Language          string            `json:"language"`
	ServiceKeys       []string          `json:"service_keys"`
	ExecutionMode     string            `json:"execution_mode"`
	FailurePolicy     string            `json:"failure_policy"`
	CompactParameters *bool             `json:"compact_parameters,omitempty"`
	Parameters        map[string]string `json:"parameters,omitempty"`
}

type reconcileResult struct {
	Project           string             `json:"project"`
	Services          int                `json:"services"`
	ValidatedBranches int                `json:"validated_branches"`
	Jobs              []reconciledJob    `json:"jobs"`
	Delivery          repositoryLocation `json:"delivery"`
}

type reconciledJob struct {
	Key        string `json:"key"`
	JenkinsJob string `json:"jenkins_job"`
	Status     string `json:"status"`
}

type repositoryLocation struct {
	Jenkinsfiles string `json:"jenkinsfiles"`
	Manifests    string `json:"manifests"`
}

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	inputPath := flag.String("input", "", "reconcile input JSON path")
	skipSourceCredential := flag.Bool("skip-source-credential", false, "save and sync jobs without rotating the source Git credential")
	flag.Parse()
	if *inputPath == "" {
		fatal(fmt.Errorf("input is required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var input reconcileInput
	payload, err := os.ReadFile(*inputPath)
	if err != nil {
		fatal(err)
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		fatal(fmt.Errorf("decode input: %w", err))
	}
	clear(payload)
	if input.Project == "" || input.ConnectionKey == "" || len(input.Services) == 0 || len(input.Jobs) == 0 {
		fatal(fmt.Errorf("project, connection_key, services and jobs are required"))
	}

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

	delivery, err := gitlabService.GetDelivery(ctx, input.Project)
	if err != nil {
		fatal(err)
	}
	if root := strings.TrimSpace(input.DockerfilesRoot); root != "" {
		for index := range input.Services {
			path := filepath.Join(root, input.Services[index].Key, "Dockerfile")
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				fatal(fmt.Errorf("read Dockerfile for %s: %w", input.Services[index].Key, readErr))
			}
			input.Services[index].DockerfileContent = string(content)
			clear(content)
		}
	}
	delivery.Services = input.Services
	delivery, err = gitlabService.SaveDelivery(ctx, input.Project, delivery)
	if err != nil {
		fatal(err)
	}
	if _, err := gitlabService.Provision(ctx, input.Project); err != nil {
		fatal(fmt.Errorf("provision delivery repositories: %w", err))
	}
	repositories, err := gitlabService.ListSourceRepositories(ctx, input.Project, "")
	if err != nil {
		fatal(fmt.Errorf("list source repositories: %w", err))
	}
	repositoryByURL := make(map[string]gitlab.SourceRepositoryOption, len(repositories))
	for _, repository := range repositories {
		repositoryByURL[repository.CloneURL] = repository
	}
	validatedBranches := 0
	for _, service := range delivery.Services {
		repository, ok := repositoryByURL[service.SourceRepository]
		if !ok {
			fatal(fmt.Errorf("source repository is not visible to the bound GitLab server: %s", service.SourceRepository))
		}
		branches, err := gitlabService.ListSourceRepositoryBranches(ctx, input.Project, repository.ProjectID)
		if err != nil {
			fatal(fmt.Errorf("list branches for %s: %w", service.Key, err))
		}
		found := false
		available := make([]string, 0, len(branches))
		for _, branch := range branches {
			available = append(available, branch.Name)
			if branch.Name == service.SourceBranch {
				found = true
				break
			}
		}
		if !found {
			fatal(fmt.Errorf("configured branch %s does not exist in service %s; available branches: %v", service.SourceBranch, service.Key, available))
		}
		validatedBranches++
	}
	deliveryCredential, err := gitlabService.SyncDeliveryCredential(ctx, input.Project, input.ConnectionKey, cicdService)
	if err != nil {
		fatal(err)
	}

	result := reconcileResult{
		Project: input.Project, Services: len(delivery.Services), ValidatedBranches: validatedBranches,
		Delivery: repositoryLocation{Jenkinsfiles: delivery.JenkinsfilesCloneURL, Manifests: delivery.ManifestsCloneURL},
	}
	for _, item := range input.Jobs {
		if !*skipSourceCredential {
			if _, err := gitlabService.SyncSourceCredential(ctx, input.Project, input.ConnectionKey, item.ServiceKeys, cicdService); err != nil {
				fatal(fmt.Errorf("sync source credential for %s: %w", item.Key, err))
			}
		}
		mode := strings.ToLower(strings.TrimSpace(item.JenkinsfileMode))
		if mode == "" {
			mode = "existing"
		}
		executionMode := strings.ToLower(strings.TrimSpace(item.ExecutionMode))
		if executionMode == "" {
			executionMode = "serial"
		}
		failurePolicy := strings.ToLower(strings.TrimSpace(item.FailurePolicy))
		if failurePolicy == "" {
			failurePolicy = "stop"
		}
		compactParameters := true
		if item.CompactParameters != nil {
			compactParameters = *item.CompactParameters
		}
		jenkinsfilePath := strings.TrimSpace(item.Jenkinsfile)
		if jenkinsfilePath == "" && mode == "generated" {
			jenkinsfilePath = "jobs/" + item.Key + "/Jenkinsfile"
		}
		job, err := cicdService.SaveJob(ctx, input.Project, item.Key, cicd.Job{
			Key: item.Key, DisplayName: item.DisplayName, ServiceName: item.Key, ServiceKeys: item.ServiceKeys,
			Language: item.Language, JenkinsfileMode: mode, ExecutionMode: executionMode, FailurePolicy: failurePolicy, CompactParameters: compactParameters,
			ConnectionKey: input.ConnectionKey, JenkinsJobName: item.JenkinsJobName, Enabled: true,
			JenkinsfileRepository: "ops-delivery-jenkinsfiles", JenkinsfileRepo: delivery.JenkinsfilesCloneURL,
			JenkinsfileBranch: delivery.DefaultBranch, JenkinsfilePath: jenkinsfilePath, JenkinsfileCredential: deliveryCredential.Key,
			ManifestRepository: "ops-delivery-manifests", ManifestRepo: delivery.ManifestsCloneURL,
			ManifestBranch: delivery.DefaultBranch, ManifestPath: "environments", ManifestCredential: deliveryCredential.Key,
			EnvironmentPaths: map[string]string{"dev": "environments/dev", "test": "environments/test", "uat": "environments/uat", "prod": "environments/prod"},
			Parameters:       item.Parameters,
		})
		if err != nil {
			fatal(fmt.Errorf("save job %s: %w", item.Key, err))
		}
		if mode == "generated" {
			job, err = cicdService.PrepareGeneratedJobRepositories(ctx, input.Project, job.Key)
			if err != nil {
				fatal(fmt.Errorf("prepare generated repositories for %s: %w", item.Key, err))
			}
			if _, _, err = gitlabService.SyncJobJenkinsfile(ctx, input.Project, job); err != nil {
				fatal(fmt.Errorf("sync generated Jenkinsfile for %s: %w", item.Key, err))
			}
		}
		job, err = cicdService.SyncJob(ctx, input.Project, job.Key)
		if err != nil {
			fatal(fmt.Errorf("sync job %s: %w", item.Key, err))
		}
		result.Jobs = append(result.Jobs, reconciledJob{Key: job.Key, JenkinsJob: job.JenkinsJobName, Status: job.SyncStatus})
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
