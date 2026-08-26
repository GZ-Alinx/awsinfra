package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/awscredentials"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	region := flag.String("region", "", "AWS region")
	timeout := flag.Duration("timeout", 2*time.Minute, "command timeout")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*region) == "" || flag.NArg() == 0 {
		fatal(fmt.Errorf("project, region and AWS CLI arguments are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
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
	credentials, err := awscredentials.New(config, store)
	if err != nil {
		fatal(err)
	}
	awsEnv, err := credentials.Environment(ctx, *project)
	if err != nil {
		fatal(err)
	}
	args := append([]string(nil), flag.Args()...)
	args = append(args, "--region", strings.TrimSpace(*region), "--no-cli-pager")
	command := exec.CommandContext(ctx, config.Tools.AWS, args...) // #nosec G204 -- explicit administrator CLI scoped to the project-bound AWS identity.
	command.Env = append(withoutAWS(os.Environ()), awsEnv...)
	command.Env = append(command.Env, "AWS_REGION="+strings.TrimSpace(*region), "AWS_DEFAULT_REGION="+strings.TrimSpace(*region))
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		fatal(fmt.Errorf("AWS CLI failed: %w", err))
	}
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
