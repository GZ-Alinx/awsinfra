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

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/gitlab"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	serviceKey := flag.String("service", "", "service key")
	repositoryPath := flag.String("repository-path", "", "checked out repository path")
	branch := flag.String("branch", "main", "target branch")
	message := flag.String("message", "", "commit message")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*serviceKey) == "" || strings.TrimSpace(*repositoryPath) == "" || strings.TrimSpace(*branch) == "" || strings.TrimSpace(*message) == "" {
		fatal(fmt.Errorf("project, service, repository-path, branch and message are required"))
	}
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
	service, err := gitlab.New(config, store)
	if err != nil {
		fatal(err)
	}
	cloneURL, username, password, err := service.SourceRelayTarget(ctx, *project, *serviceKey)
	if err != nil {
		fatal(err)
	}
	defer clear(password)
	origin, err := output(ctx, *repositoryPath, nil, "git", "remote", "get-url", "origin")
	if err != nil || strings.TrimSuffix(strings.TrimSpace(origin), "/") != strings.TrimSuffix(strings.TrimSpace(cloneURL), "/") {
		fatal(fmt.Errorf("repository origin does not match the project source registration"))
	}
	if _, err := output(ctx, *repositoryPath, nil, "git", "add", "--all"); err != nil {
		fatal(err)
	}
	status, err := output(ctx, *repositoryPath, nil, "git", "status", "--porcelain")
	if err != nil {
		fatal(err)
	}
	if strings.TrimSpace(status) == "" {
		fmt.Println("no source changes to push")
		return
	}
	identity := []string{"GIT_AUTHOR_NAME=AWSInfra", "GIT_AUTHOR_EMAIL=ops-deploy@local", "GIT_COMMITTER_NAME=AWSInfra", "GIT_COMMITTER_EMAIL=ops-deploy@local"}
	if commitOutput, err := output(ctx, *repositoryPath, identity, "git", "commit", "-m", strings.TrimSpace(*message)); err != nil {
		fatal(fmt.Errorf("commit source changes: %w: %s", err, strings.TrimSpace(commitOutput)))
	}
	helperDir, err := os.MkdirTemp("", "ops-git-askpass-")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(helperDir)
	helper := filepath.Join(helperDir, "askpass.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' \"$OPS_GIT_USERNAME\";; *) printf '%s\\n' \"$OPS_GIT_PASSWORD\";; esac\n"), 0o700); err != nil {
		fatal(err)
	}
	environment := []string{"GIT_ASKPASS=" + helper, "GIT_TERMINAL_PROMPT=0", "OPS_GIT_USERNAME=" + username, "OPS_GIT_PASSWORD=" + string(password)}
	pushOutput, err := output(ctx, *repositoryPath, environment, "git", "push", "origin", "HEAD:refs/heads/"+strings.TrimSpace(*branch))
	clear(password)
	if err != nil {
		fatal(fmt.Errorf("push source changes: %w: %s", err, strings.TrimSpace(pushOutput)))
	}
	head, err := output(ctx, *repositoryPath, nil, "git", "rev-parse", "--short", "HEAD")
	if err != nil {
		fatal(err)
	}
	fmt.Printf("project=%s service=%s branch=%s commit=%s\n", *project, *serviceKey, *branch, strings.TrimSpace(head))
}

func output(ctx context.Context, directory string, extraEnv []string, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), extraEnv...)
	data, err := command.CombinedOutput()
	return string(data), err
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
