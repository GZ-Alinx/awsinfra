package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/persistence"
	"ops-deploy-platform/internal/statebackend"
)

type stateDocument struct {
	Resources []struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Instances []struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"instances"`
	} `json:"resources"`
}

type auditResult struct {
	Address                  string `json:"address"`
	Identifier               string `json:"identifier,omitempty"`
	ManageMasterUserPassword bool   `json:"manage_master_user_password"`
	MasterPasswordPresent    bool   `json:"master_password_present"`
	MasterSecretPresent      bool   `json:"master_secret_present"`
}

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	target := flag.String("target", "", "environment target")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*target) == "" {
		fatal(fmt.Errorf("project and target are required"))
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
	service, err := statebackend.New(config, store)
	if err != nil {
		fatal(err)
	}
	runtime, err := service.Runtime(ctx)
	if err != nil {
		fatal(err)
	}
	defer clear([]byte(runtime.SecretAccessKey))
	defer clear([]byte(runtime.SessionToken))
	objectKey := strings.Trim(runtime.KeyPrefix, "/") + "/projects/" + strings.ToLower(strings.TrimSpace(*project)) + "/" + strings.ToLower(strings.TrimSpace(*target)) + "/infra/terraform.tfstate"
	command := exec.CommandContext(ctx, config.Tools.AWS, "s3", "cp", "s3://"+runtime.Bucket+"/"+objectKey, "-", "--region", runtime.Region, "--only-show-errors", "--no-progress") // #nosec G204 -- fixed AWS CLI with validated platform scopes.
	command.Env = append(os.Environ(), "AWS_ACCESS_KEY_ID="+runtime.AccessKeyID, "AWS_SECRET_ACCESS_KEY="+runtime.SecretAccessKey, "AWS_REGION="+runtime.Region, "AWS_DEFAULT_REGION="+runtime.Region)
	if runtime.SessionToken != "" {
		command.Env = append(command.Env, "AWS_SESSION_TOKEN="+runtime.SessionToken)
	}
	payload, err := command.Output()
	if err != nil {
		fatal(fmt.Errorf("read centralized Terraform state: %w", err))
	}
	defer clear(payload)
	var document stateDocument
	if err := json.Unmarshal(payload, &document); err != nil {
		fatal(err)
	}
	results := []auditResult{}
	for _, resource := range document.Resources {
		if resource.Type != "aws_rds_cluster" && resource.Type != "aws_db_instance" {
			continue
		}
		for _, instance := range resource.Instances {
			attributes := instance.Attributes
			password, _ := attributes["master_password"].(string)
			secret, _ := attributes["master_user_secret"].([]any)
			identifier, _ := attributes["cluster_identifier"].(string)
			if identifier == "" {
				identifier, _ = attributes["identifier"].(string)
			}
			managed, _ := attributes["manage_master_user_password"].(bool)
			results = append(results, auditResult{Address: resource.Type + "." + resource.Name, Identifier: identifier, ManageMasterUserPassword: managed, MasterPasswordPresent: strings.TrimSpace(password) != "", MasterSecretPresent: len(secret) > 0})
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
