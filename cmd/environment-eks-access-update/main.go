package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/access"
	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

type cidrList []string

func (values *cidrList) String() string { return strings.Join(*values, ",") }

func (values *cidrList) Set(value string) error {
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if _, _, err := net.ParseCIDR(item); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", item, err)
		}
		*values = append(*values, item)
	}
	return nil
}

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	environmentKey := flag.String("environment", "", "environment key")
	public := flag.Bool("public", false, "enable the public EKS API endpoint")
	private := flag.Bool("private", true, "enable the private EKS API endpoint")
	var publicCIDRs cidrList
	flag.Var(&publicCIDRs, "public-cidr", "allowed public API CIDR; repeat for multiple values")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*environmentKey) == "" {
		fatal(fmt.Errorf("project and environment are required"))
	}
	if !*public && !*private {
		fatal(fmt.Errorf("at least one EKS API endpoint must remain enabled"))
	}
	if *public && len(publicCIDRs) == 0 {
		fatal(fmt.Errorf("public EKS API requires at least one explicit public-cidr"))
	}
	if !*public && len(publicCIDRs) > 0 {
		fatal(fmt.Errorf("public-cidr cannot be used while the public endpoint is disabled"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	accessService := access.NewService(store)
	target, err := accessService.Environment(ctx, *project, *environmentKey)
	if err != nil {
		fatal(err)
	}
	repository, err := environment.NewRepositoryWithStore(config.Paths.EnvironmentsDir, store)
	if err != nil {
		fatal(err)
	}
	document, err := repository.Load(target.TargetName)
	if err != nil {
		fatal(err)
	}
	document = environment.ApplyDefaults(document, target.ProjectKey, target.Environment)
	eks, ok := document["eks"].(map[string]any)
	if !ok {
		fatal(fmt.Errorf("environment EKS configuration is missing"))
	}
	eks["endpoint_public_access"] = *public
	eks["endpoint_private_access"] = *private
	values := make([]any, 0, len(publicCIDRs))
	for _, cidr := range publicCIDRs {
		values = append(values, cidr)
	}
	eks["public_access_cidrs"] = values
	if err := environment.Validate(document); err != nil {
		fatal(fmt.Errorf("updated environment configuration is invalid: %w", err))
	}
	if err := repository.Save(target.TargetName, document); err != nil {
		fatal(err)
	}
	fmt.Printf("project=%s environment=%s public=%t private=%t public_cidrs=%s\n", target.ProjectKey, target.Environment, *public, *private, strings.Join(publicCIDRs, ","))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
