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

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/cicd"
	"ops-deploy-platform/internal/gitlab"
	"ops-deploy-platform/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	serviceKey := flag.String("service", "", "service key")
	repository := flag.String("repository", "", "source repository clone URL")
	branch := flag.String("branch", "main", "source branch")
	destination := flag.String("destination", "", "empty checkout destination")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*serviceKey) == "" || strings.TrimSpace(*repository) == "" || strings.TrimSpace(*destination) == "" {
		fatal(fmt.Errorf("project, service, repository and destination are required"))
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
	key := "source-" + strings.ToLower(strings.TrimSpace(*serviceKey))
	if len(key) > 63 {
		key = key[:63]
	}
	if err := store.SaveCICDRepository(ctx, cicd.Repository{
		Key: key, ProjectKey: strings.ToLower(strings.TrimSpace(*project)), DisplayName: strings.TrimSpace(*serviceKey) + " 业务源码",
		Provider: "gitlab", Purpose: "source", CloneURL: strings.TrimSpace(*repository), DefaultBranch: strings.TrimSpace(*branch),
		Description: "来自项目绑定的业务源码 GitLab，只允许当前项目流水线读取",
	}); err != nil {
		fatal(err)
	}
	service, err := gitlab.New(config, store)
	if err != nil {
		fatal(err)
	}
	cloneURL, username, password, err := service.SourceRelayTarget(ctx, *project, *serviceKey)
	if err != nil {
		fatal(err)
	}
	defer clear(password)
	if _, err := os.Stat(*destination); err == nil || !os.IsNotExist(err) {
		fatal(fmt.Errorf("destination must not exist"))
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
	command := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--single-branch", "--branch", strings.TrimSpace(*branch), cloneURL, *destination)
	command.Env = append(os.Environ(), "GIT_ASKPASS="+helper, "GIT_TERMINAL_PROMPT=0", "OPS_GIT_USERNAME="+username, "OPS_GIT_PASSWORD="+string(password))
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		fatal(fmt.Errorf("clone source repository: %w", err))
	}
	clear(password)
	fmt.Printf("project=%s service=%s branch=%s destination=%s\n", *project, *serviceKey, *branch, *destination)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
