package main

import (
	"bytes"
	"context"
	"encoding/json"
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

type natGateway struct {
	ID       string `json:"NatGatewayId"`
	VPCID    string `json:"VpcId"`
	SubnetID string `json:"SubnetId"`
	PublicIP string `json:"PublicIp"`
	State    string `json:"State"`
}

type route struct {
	RouteTableID string `json:"RouteTableId"`
	SubnetID     string `json:"SubnetId"`
	Destination  string `json:"Destination"`
	Target       string `json:"Target"`
}

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	region := flag.String("region", "", "AWS region")
	publicIP := flag.String("public-ip", "", "expected NAT public IP")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*region) == "" || strings.TrimSpace(*publicIP) == "" {
		fatal(fmt.Errorf("project, region and public-ip are required"))
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
	credentials, err := awscredentials.New(config, store)
	if err != nil {
		fatal(err)
	}
	environment, err := credentials.Environment(ctx, *project)
	if err != nil {
		fatal(err)
	}
	natPayload := runAWS(ctx, config.Tools.AWS, environment,
		"ec2", "describe-nat-gateways", "--region", *region,
		"--filter", "Name=state,Values=available",
		"--query", "NatGateways[].{NatGatewayId:NatGatewayId,VpcId:VpcId,SubnetId:SubnetId,PublicIp:NatGatewayAddresses[0].PublicIp,State:State}",
		"--output", "json", "--no-cli-pager")
	var available []natGateway
	if err := json.Unmarshal(natPayload, &available); err != nil {
		fatal(err)
	}
	gateways := make([]natGateway, 0, 1)
	for _, gateway := range available {
		if gateway.PublicIP == *publicIP {
			gateways = append(gateways, gateway)
		}
	}
	if len(gateways) != 1 {
		fatal(fmt.Errorf("expected one available NAT gateway for %s, got %d", *publicIP, len(gateways)))
	}
	routePayload := runAWS(ctx, config.Tools.AWS, environment,
		"ec2", "describe-route-tables", "--region", *region,
		"--filters", "Name=vpc-id,Values="+gateways[0].VPCID,
		"--output", "json", "--no-cli-pager")
	var response struct {
		RouteTables []struct {
			ID           string `json:"RouteTableId"`
			Associations []struct {
				SubnetID string `json:"SubnetId"`
			} `json:"Associations"`
			Routes []struct {
				Destination string `json:"DestinationCidrBlock"`
				NATGateway  string `json:"NatGatewayId"`
				Gateway     string `json:"GatewayId"`
			} `json:"Routes"`
		} `json:"RouteTables"`
	}
	if err := json.Unmarshal(routePayload, &response); err != nil {
		fatal(err)
	}
	routes := make([]route, 0)
	for _, table := range response.RouteTables {
		for _, association := range table.Associations {
			for _, item := range table.Routes {
				if item.Destination != "0.0.0.0/0" {
					continue
				}
				target := item.NATGateway
				if target == "" {
					target = item.Gateway
				}
				routes = append(routes, route{RouteTableID: table.ID, SubnetID: association.SubnetID, Destination: item.Destination, Target: target})
			}
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"nat_gateway": gateways[0], "default_routes": routes})
}

func runAWS(ctx context.Context, tool string, projectEnvironment []string, args ...string) []byte {
	cmd := exec.CommandContext(ctx, tool, args...) // #nosec G204 -- arguments are fixed except validated command flags.
	cmd.Env = cleanEnvironment(os.Environ())
	cmd.Env = append(cmd.Env, projectEnvironment...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		fatal(fmt.Errorf("aws command failed: %v: %s", err, strings.TrimSpace(stderr.String())))
	}
	return stdout.Bytes()
}

func cleanEnvironment(environment []string) []string {
	blocked := map[string]bool{"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AWS_PROFILE": true, "AWS_DEFAULT_PROFILE": true}
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		key, _, _ := strings.Cut(value, "=")
		if !blocked[key] {
			result = append(result, value)
		}
	}
	return result
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
