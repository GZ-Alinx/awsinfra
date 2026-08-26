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

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	target := flag.String("target", "", "environment target")
	stage := flag.String("stage", "platform", "Terraform stage")
	lockID := flag.String("lock-id", "", "expected Terraform lock ID")
	confirm := flag.String("confirm", "", "required confirmation: unlock:<project>/<target>/<stage>")
	flag.Parse()
	projectKey := strings.ToLower(strings.TrimSpace(*project))
	targetKey := strings.ToLower(strings.TrimSpace(*target))
	stageKey := strings.ToLower(strings.TrimSpace(*stage))
	if projectKey == "" || targetKey == "" || (stageKey != "infra" && stageKey != "platform") || strings.TrimSpace(*lockID) == "" {
		fatal(fmt.Errorf("project, target, valid stage and lock-id are required"))
	}
	if *confirm != "unlock:"+projectKey+"/"+targetKey+"/"+stageKey {
		fatal(fmt.Errorf("unlock confirmation does not match"))
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
	key := strings.Trim(runtime.KeyPrefix, "/") + "/projects/" + projectKey + "/" + targetKey + "/" + stageKey + "/terraform.tfstate.tflock"
	environment := append(os.Environ(), "AWS_ACCESS_KEY_ID="+runtime.AccessKeyID, "AWS_SECRET_ACCESS_KEY="+runtime.SecretAccessKey, "AWS_REGION="+runtime.Region, "AWS_DEFAULT_REGION="+runtime.Region)
	if runtime.SessionToken != "" {
		environment = append(environment, "AWS_SESSION_TOKEN="+runtime.SessionToken)
	}
	payload, err := awsOutput(ctx, config.Tools.AWS, environment, "s3", "cp", "s3://"+runtime.Bucket+"/"+key, "-", "--region", runtime.Region, "--only-show-errors", "--no-progress")
	if err != nil {
		fatal(fmt.Errorf("read Terraform lock: %w", err))
	}
	defer clear(payload)
	var lock struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(payload, &lock); err != nil || lock.ID != strings.TrimSpace(*lockID) {
		fatal(fmt.Errorf("Terraform lock ID changed; refusing to unlock"))
	}
	if _, err := awsOutput(ctx, config.Tools.AWS, environment, "s3api", "delete-object", "--bucket", runtime.Bucket, "--key", key, "--region", runtime.Region, "--no-cli-pager"); err != nil {
		fatal(fmt.Errorf("delete Terraform lock: %w", err))
	}
	fmt.Printf("unlocked project=%s target=%s stage=%s lock_id=%s\n", projectKey, targetKey, stageKey, lock.ID)
}

func awsOutput(ctx context.Context, tool string, environment []string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, tool, args...) // #nosec G204 -- fixed AWS CLI with validated platform state scope.
	command.Env = environment
	return command.Output()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
