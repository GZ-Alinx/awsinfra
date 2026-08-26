package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/environment"
)

func TestTerraformDoesNotOwnEnvironmentScopedECRRepositories(t *testing.T) {
	payload, err := os.ReadFile("../../terraform/infra/ecr.tf")
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	if strings.Contains(source, `resource "aws_ecr_repository"`) || strings.Contains(source, `resource "aws_ecr_lifecycle_policy"`) {
		t.Fatalf("environment Terraform must not own shared ECR repositories: %s", source)
	}
	for _, migration := range []string{"from = aws_ecr_repository.this", "from = aws_ecr_lifecycle_policy.this", "destroy = false"} {
		if !strings.Contains(source, migration) {
			t.Fatalf("missing non-destructive ECR state migration %q", migration)
		}
	}
}

type sharedECRExecutor struct {
	commands []Command
	missing  bool
}

func (e *sharedECRExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	e.commands = append(e.commands, command)
	if e.missing && slices.Contains(command.Args, "describe-repositories") {
		_, _ = io.WriteString(output, "RepositoryNotFoundException: repository does not exist")
		return errors.New("exit status 254")
	}
	return nil
}

func TestEnsureSharedECRRepositoriesReusesExistingRepository(t *testing.T) {
	doc := environment.DefaultDocument("demo", "prod")
	doc["region"] = "ap-south-1"
	ecr := doc["ecr"].(map[string]any)
	ecr["enabled"] = true
	ecr["repositories"] = []any{"gateway"}
	executor := &sharedECRExecutor{}
	deployment := &Deployment{
		config: &appconfig.Config{
			Paths: appconfig.PathsConfig{RepositoryRoot: "."},
			Tools: appconfig.ToolsConfig{AWS: "aws"},
		},
		executor: executor,
	}
	var output bytes.Buffer
	if err := deployment.ensureSharedECRRepositories(context.Background(), doc, &output); err != nil {
		t.Fatal(err)
	}
	for _, unexpected := range executor.commands {
		if slices.Contains(unexpected.Args, "create-repository") {
			t.Fatalf("existing project repository was recreated: %#v", unexpected.Args)
		}
	}
	if !strings.Contains(output.String(), "[已复用] demo/gateway") || !strings.Contains(output.String(), "其他环境的仓库不会被修改") {
		t.Fatalf("operator-facing reuse log is incomplete: %s", output.String())
	}
}

func TestEnsureSharedECRRepositoriesCreatesOnlyWhenMissing(t *testing.T) {
	doc := environment.DefaultDocument("demo", "prod")
	doc["region"] = "ap-south-1"
	ecr := doc["ecr"].(map[string]any)
	ecr["enabled"] = true
	ecr["repositories"] = []any{"gateway"}
	executor := &sharedECRExecutor{missing: true}
	deployment := &Deployment{
		config: &appconfig.Config{
			Paths: appconfig.PathsConfig{RepositoryRoot: "."},
			Tools: appconfig.ToolsConfig{AWS: "aws"},
		},
		executor: executor,
	}
	var output bytes.Buffer
	if err := deployment.ensureSharedECRRepositories(context.Background(), doc, &output); err != nil {
		t.Fatal(err)
	}
	createCount := 0
	for _, command := range executor.commands {
		if slices.Contains(command.Args, "create-repository") {
			createCount++
			if !slices.Contains(command.Args, "demo/gateway") {
				t.Fatalf("project prefix missing from repository: %#v", command.Args)
			}
		}
	}
	if createCount != 1 || !strings.Contains(output.String(), "[已创建] demo/gateway") {
		t.Fatalf("missing repository was not created exactly once: commands=%#v output=%s", executor.commands, output.String())
	}
}
