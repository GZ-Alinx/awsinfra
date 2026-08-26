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

// cicd-build-trigger is an operator-safe equivalent of the platform build
// button. It is useful for end-to-end release verification without handling or
// printing Jenkins credentials outside the platform process.
func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	job := flag.String("job", "", "platform Job key")
	environment := flag.String("environment", "", "environment key")
	services := flag.String("services", "", "comma-separated service keys")
	branch := flag.String("branch", "", "source branch")
	requestedBy := flag.String("requested-by", "platform-operator", "audit username")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*job) == "" || strings.TrimSpace(*environment) == "" || strings.TrimSpace(*services) == "" {
		fatal(fmt.Errorf("project, job, environment and services are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	service, err := cicd.New(config, store)
	if err != nil {
		fatal(err)
	}
	awsService, err := awscredentials.New(config, store)
	if err != nil {
		fatal(err)
	}
	tunnels := kubetunnel.New(config, awsService)
	defer tunnels.Close()
	service.SetTunnelProvider(tunnels)
	build, err := service.TriggerBuild(ctx, strings.TrimSpace(*project), strings.TrimSpace(*job), strings.TrimSpace(*requestedBy), cicd.BuildInput{
		Environment: strings.TrimSpace(*environment),
		Services:    split(*services),
		Branch:      strings.TrimSpace(*branch),
	})
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(build); err != nil {
		fatal(err)
	}
}

func split(value string) []string {
	items := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
