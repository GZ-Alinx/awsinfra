package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/access"
	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/awscredentials"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	environmentKey := flag.String("environment", "", "environment key")
	profile := flag.String("profile", "isolated-production", "scheduling profile")
	apply := flag.Bool("apply", false, "persist the profile after writing a backup")
	syncAWSCapacity := flag.Bool("sync-aws-capacity", false, "preserve the actual min/max capacity of node groups that already exist in AWS")
	deferCapacity := flag.Bool("defer-capacity", false, "create isolated node groups at zero initial capacity until the regional EC2 quota is approved")
	inspectAWS := flag.Bool("inspect-aws", false, "print the live EKS node group and optional quota request status without changing configuration")
	quotaRequestID := flag.String("quota-request-id", "", "service quota request ID to include with --inspect-aws")
	deleteUnreadyIsolated := flag.Bool("delete-unready-isolated", false, "delete only unready isolated node groups so they can be recreated safely")
	bridgeLegacyPlatform := flag.Bool("bridge-legacy-platform-nodes", false, "temporarily label legacy ops nodes for the platform-ops scheduling transition")
	backupDir := flag.String("backup-dir", "/app/data/config-backups", "backup directory")
	flag.Parse()
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*environmentKey) == "" {
		fatal(fmt.Errorf("project and environment are required"))
	}
	if *profile != "isolated-production" {
		fatal(fmt.Errorf("unsupported profile %q", *profile))
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
	target, err := access.NewService(store).Environment(ctx, *project, *environmentKey)
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

	if *bridgeLegacyPlatform {
		credentials, credentialErr := awscredentials.New(config, store)
		if credentialErr != nil {
			fatal(credentialErr)
		}
		awsEnv, credentialErr := credentials.Environment(ctx, *project)
		if credentialErr != nil {
			fatal(credentialErr)
		}
		if err := bridgeLegacyPlatformNodes(ctx, config, document, awsEnv); err != nil {
			fatal(err)
		}
		return
	}
	if *deleteUnreadyIsolated {
		credentials, credentialErr := awscredentials.New(config, store)
		if credentialErr != nil {
			fatal(credentialErr)
		}
		awsEnv, credentialErr := credentials.Environment(ctx, *project)
		if credentialErr != nil {
			fatal(credentialErr)
		}
		deleted, deleteErr := deleteUnreadyIsolatedNodeGroups(ctx, config, document, awsEnv)
		if deleteErr != nil {
			fatal(deleteErr)
		}
		fmt.Printf("delete_requested=%v\n", deleted)
		return
	}
	if *inspectAWS {
		credentials, credentialErr := awscredentials.New(config, store)
		if credentialErr != nil {
			fatal(credentialErr)
		}
		awsEnv, credentialErr := credentials.Environment(ctx, *project)
		if credentialErr != nil {
			fatal(credentialErr)
		}
		if err := inspectAWSState(ctx, config, document, awsEnv, strings.TrimSpace(*quotaRequestID)); err != nil {
			fatal(err)
		}
		return
	}
	if !*apply {
		printSummary(target, document, false, "")
		return
	}
	backupPath, err := backupDocument(*backupDir, target.TargetName, document)
	if err != nil {
		fatal(err)
	}
	if *syncAWSCapacity {
		credentials, credentialErr := awscredentials.New(config, store)
		if credentialErr != nil {
			fatal(credentialErr)
		}
		awsEnv, credentialErr := credentials.Environment(ctx, *project)
		if credentialErr != nil {
			fatal(credentialErr)
		}
		if _, syncErr := syncExistingNodeGroupCapacity(ctx, config, document, awsEnv); syncErr != nil {
			fatal(syncErr)
		}
	}
	if err := applyIsolatedProduction(document); err != nil {
		fatal(err)
	}
	if *deferCapacity {
		deferIsolatedNodeGroupCapacity(document)
	}
	if err := repository.Save(target.TargetName, document); err != nil {
		fatal(err)
	}
	printSummary(target, document, true, backupPath)
}

func bridgeLegacyPlatformNodes(ctx context.Context, config *appconfig.Config, document environment.Document, awsEnv []string) error {
	region, _ := document["region"].(string)
	cluster := environment.ClusterName(document)
	if strings.TrimSpace(region) == "" || strings.TrimSpace(cluster) == "" {
		return fmt.Errorf("region and cluster name are required to bridge legacy platform nodes")
	}
	file, err := os.CreateTemp("", "ops-deploy-kubeconfig-*")
	if err != nil {
		return fmt.Errorf("create temporary kubeconfig: %w", err)
	}
	path := file.Name()
	_ = file.Close()
	defer os.Remove(path)
	if err := runAWSNoOutput(ctx, config, awsEnv, region, "eks", "update-kubeconfig", "--name", cluster, "--alias", cluster, "--kubeconfig", path); err != nil {
		return fmt.Errorf("write temporary kubeconfig: %w", err)
	}
	args := []string{"--kubeconfig", path, "label", "nodes", "-l", "eks.amazonaws.com/nodegroup=ops", "ops-deploy.io/pool=platform-ops", "--overwrite"}
	command := exec.CommandContext(ctx, config.Tools.Kubectl, args...) // #nosec G204 -- fixed kubectl operation scoped to the selected cluster.
	command.Env = append(withoutAWS(os.Environ()), awsEnv...)
	command.Env = append(command.Env, "AWS_REGION="+region, "AWS_DEFAULT_REGION="+region)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("label legacy ops nodes: %w: %s", err, strings.TrimSpace(string(output)))
	}
	fmt.Printf("legacy_platform_bridge=applied selector=eks.amazonaws.com/nodegroup=ops label=ops-deploy.io/pool\n%s", string(output))
	for _, args := range [][]string{
		{"--kubeconfig", path, "get", "nodes", "-L", "eks.amazonaws.com/nodegroup,workload-class,ops-deploy.io/pool"},
		{"--kubeconfig", path, "get", "pods", "-A", "--field-selector=status.phase=Pending", "-o", "wide"},
		{"--kubeconfig", path, "get", "pods", "-A", "-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,READY:.status.containerStatuses[*].ready,PHASE:.status.phase,NODE:.spec.nodeName"},
	} {
		inspect := exec.CommandContext(ctx, config.Tools.Kubectl, args...) // #nosec G204 -- fixed read-only kubectl inspection.
		inspect.Env = append(withoutAWS(os.Environ()), awsEnv...)
		inspect.Env = append(inspect.Env, "AWS_REGION="+region, "AWS_DEFAULT_REGION="+region)
		payload, inspectErr := inspect.CombinedOutput()
		if inspectErr != nil {
			return fmt.Errorf("inspect bridged Kubernetes nodes: %w: %s", inspectErr, strings.TrimSpace(string(payload)))
		}
		fmt.Print(string(payload))
	}
	return nil
}

func deleteUnreadyIsolatedNodeGroups(ctx context.Context, config *appconfig.Config, document environment.Document, awsEnv []string) ([]string, error) {
	region, _ := document["region"].(string)
	cluster := environment.ClusterName(document)
	if strings.TrimSpace(region) == "" || strings.TrimSpace(cluster) == "" {
		return nil, fmt.Errorf("region and cluster name are required to recover isolated node groups")
	}
	allowed := map[string]bool{"CREATING": true, "CREATE_FAILED": true, "DEGRADED": true, "DELETE_FAILED": true}
	deleted := make([]string, 0, 3)
	for _, name := range []string{"ingress-gateway", "business-workload", "platform-ops"} {
		var described struct {
			Nodegroup struct {
				Status string `json:"status"`
			} `json:"nodegroup"`
		}
		err := runAWSJSON(ctx, config, awsEnv, region, &described, "eks", "describe-nodegroup", "--cluster-name", cluster, "--nodegroup-name", name)
		if err != nil {
			if strings.Contains(err.Error(), "ResourceNotFoundException") {
				continue
			}
			return nil, fmt.Errorf("inspect isolated EKS node group %s: %w", name, err)
		}
		if !allowed[described.Nodegroup.Status] {
			return nil, fmt.Errorf("refuse to delete isolated EKS node group %s in status %s", name, described.Nodegroup.Status)
		}
		if err := runAWSNoOutput(ctx, config, awsEnv, region, "eks", "delete-nodegroup", "--cluster-name", cluster, "--nodegroup-name", name); err != nil {
			return nil, fmt.Errorf("delete isolated EKS node group %s: %w", name, err)
		}
		deleted = append(deleted, name)
	}
	return deleted, nil
}

func runAWSNoOutput(ctx context.Context, config *appconfig.Config, awsEnv []string, region string, args ...string) error {
	args = append(args, "--region", region, "--no-cli-pager")
	command := exec.CommandContext(ctx, config.Tools.AWS, args...) // #nosec G204 -- fixed AWS CLI operations with validated platform values.
	command.Env = append(withoutAWS(os.Environ()), awsEnv...)
	command.Env = append(command.Env, "AWS_REGION="+region, "AWS_DEFAULT_REGION="+region)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type nodeGroupInspection struct {
	Name    string           `json:"name"`
	Status  string           `json:"status"`
	Scaling nodeGroupScaling `json:"scaling"`
	Issues  []map[string]any `json:"issues,omitempty"`
	ASGs    []map[string]any `json:"auto_scaling_groups,omitempty"`
}

func inspectAWSState(ctx context.Context, config *appconfig.Config, document environment.Document, awsEnv []string, quotaRequestID string) error {
	region, _ := document["region"].(string)
	cluster := environment.ClusterName(document)
	if strings.TrimSpace(region) == "" || strings.TrimSpace(cluster) == "" {
		return fmt.Errorf("region and cluster name are required to inspect AWS")
	}
	var listed struct {
		Nodegroups []string `json:"nodegroups"`
	}
	if err := runAWSJSON(ctx, config, awsEnv, region, &listed, "eks", "list-nodegroups", "--cluster-name", cluster); err != nil {
		return fmt.Errorf("list EKS node groups: %w", err)
	}
	sort.Strings(listed.Nodegroups)
	result := struct {
		Cluster      string                `json:"cluster"`
		Region       string                `json:"region"`
		NodeGroups   []nodeGroupInspection `json:"node_groups"`
		QuotaRequest map[string]any        `json:"quota_request,omitempty"`
	}{Cluster: cluster, Region: region}
	for _, name := range listed.Nodegroups {
		var described struct {
			Nodegroup struct {
				Name    string           `json:"nodegroupName"`
				Status  string           `json:"status"`
				Scaling nodeGroupScaling `json:"scalingConfig"`
				Health  struct {
					Issues []map[string]any `json:"issues"`
				} `json:"health"`
				Resources struct {
					ASGs []map[string]any `json:"autoScalingGroups"`
				} `json:"resources"`
			} `json:"nodegroup"`
		}
		if err := runAWSJSON(ctx, config, awsEnv, region, &described, "eks", "describe-nodegroup", "--cluster-name", cluster, "--nodegroup-name", name); err != nil {
			return fmt.Errorf("describe EKS node group %s: %w", name, err)
		}
		result.NodeGroups = append(result.NodeGroups, nodeGroupInspection{
			Name: described.Nodegroup.Name, Status: described.Nodegroup.Status, Scaling: described.Nodegroup.Scaling,
			Issues: described.Nodegroup.Health.Issues, ASGs: described.Nodegroup.Resources.ASGs,
		})
	}
	if quotaRequestID != "" {
		var quota struct {
			RequestedQuota map[string]any `json:"RequestedQuota"`
		}
		if err := runAWSJSON(ctx, config, awsEnv, region, &quota, "service-quotas", "get-requested-service-quota-change", "--request-id", quotaRequestID); err != nil {
			return fmt.Errorf("read service quota request: %w", err)
		}
		result.QuotaRequest = quota.RequestedQuota
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode AWS inspection: %w", err)
	}
	fmt.Println(string(encoded))
	return nil
}

func deferIsolatedNodeGroupCapacity(document environment.Document) {
	groups := object(object(document, "eks"), "node_groups")
	for _, name := range []string{"ingress-gateway", "business-workload", "platform-ops"} {
		group, ok := groups[name].(map[string]any)
		if !ok {
			continue
		}
		group["capacity_deferred"] = true
	}
}

type nodeGroupScaling struct {
	MinSize     int `json:"minSize"`
	DesiredSize int `json:"desiredSize"`
	MaxSize     int `json:"maxSize"`
}

func syncExistingNodeGroupCapacity(ctx context.Context, config *appconfig.Config, document environment.Document, awsEnv []string) ([]string, error) {
	region, _ := document["region"].(string)
	cluster := environment.ClusterName(document)
	if strings.TrimSpace(region) == "" || strings.TrimSpace(cluster) == "" {
		return nil, fmt.Errorf("region and cluster name are required to sync AWS capacity")
	}
	var listed struct {
		Nodegroups []string `json:"nodegroups"`
	}
	if err := runAWSJSON(ctx, config, awsEnv, region, &listed, "eks", "list-nodegroups", "--cluster-name", cluster); err != nil {
		return nil, fmt.Errorf("list existing EKS node groups: %w", err)
	}
	existing := make(map[string]struct{}, len(listed.Nodegroups))
	for _, name := range listed.Nodegroups {
		existing[name] = struct{}{}
	}
	groups := object(object(document, "eks"), "node_groups")
	updated := make([]string, 0)
	for name, raw := range groups {
		if _, found := existing[name]; !found {
			continue
		}
		group, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var described struct {
			Nodegroup struct {
				Scaling nodeGroupScaling `json:"scalingConfig"`
			} `json:"nodegroup"`
		}
		if err := runAWSJSON(ctx, config, awsEnv, region, &described, "eks", "describe-nodegroup", "--cluster-name", cluster, "--nodegroup-name", name); err != nil {
			return nil, fmt.Errorf("read EKS node group %s capacity: %w", name, err)
		}
		group["min_size"] = described.Nodegroup.Scaling.MinSize
		// desired_size is observation-only in normal deployments, but keeping the
		// document current makes the plan and UI easier to understand.
		group["desired_size"] = described.Nodegroup.Scaling.DesiredSize
		group["max_size"] = described.Nodegroup.Scaling.MaxSize
		updated = append(updated, name)
	}
	sort.Strings(updated)
	return updated, nil
}

func runAWSJSON(ctx context.Context, config *appconfig.Config, awsEnv []string, region string, target any, args ...string) error {
	args = append(args, "--region", region, "--output", "json", "--no-cli-pager")
	command := exec.CommandContext(ctx, config.Tools.AWS, args...) // #nosec G204 -- fixed AWS CLI operations with validated platform values.
	command.Env = append(withoutAWS(os.Environ()), awsEnv...)
	command.Env = append(command.Env, "AWS_REGION="+region, "AWS_DEFAULT_REGION="+region)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("parse AWS response: %w", err)
	}
	return nil
}

func withoutAWS(source []string) []string {
	blocked := map[string]bool{
		"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
		"AWS_PROFILE": true, "AWS_DEFAULT_PROFILE": true, "AWS_REGION": true, "AWS_DEFAULT_REGION": true,
	}
	result := make([]string, 0, len(source))
	for _, item := range source {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			result = append(result, item)
		}
	}
	return result
}

func applyIsolatedProduction(document environment.Document) error {
	region, _ := document["region"].(string)
	if region == "" {
		return fmt.Errorf("environment region is missing")
	}
	network := object(document, "network")
	zones, ok := network["availability_zones"].([]any)
	if !ok || len(zones) < 2 {
		return fmt.Errorf("at least two availability zones are required")
	}
	eks := object(document, "eks")
	eks["workload_scheduling"] = map[string]any{"enabled": true}
	groups := object(eks, "node_groups")

	// max_size is the planned ceiling, while min/desired are deliberately kept
	// small for a staged migration. Operators can raise desired capacity after
	// each workload class has been verified on its dedicated pool.
	upsertNewGroup(groups, "ingress-gateway", zones, []any{"m7i.2xlarge"}, 2, 2, 12, 100, "gateway", "ingress-gateway")
	upsertNewGroup(groups, "business-workload", zones, []any{"m7i.4xlarge"}, 0, 0, 20, 120, "application", "business-workload")
	upsertNewGroup(groups, "platform-ops", zones, []any{"m7i.2xlarge"}, 1, 1, 6, 120, "platform", "platform-ops")

	components := object(document, "components")
	catalog := object(components, "catalog")
	higress := object(catalog, "higress")
	higress["enabled"] = true
	higress["namespace"] = "higress-system"
	higress["deployment_mode"] = "cluster"
	higress["replicas"] = 3
	higress["replica_paths"] = []any{"higress-core.gateway.replicas"}
	values := object(higress, "values")
	core := object(values, "higress-core")
	gateway := object(core, "gateway")
	gateway["replicas"] = 3
	gateway["rollingMaxUnavailable"] = 0
	gateway["rollingMaxSurge"] = "25%"
	gateway["resources"] = map[string]any{
		"requests": map[string]any{"cpu": "2", "memory": "2Gi"},
		"limits":   map[string]any{"cpu": "2", "memory": "2Gi"},
	}
	gateway["autoscaling"] = map[string]any{
		"enabled": true, "minReplicas": 3, "maxReplicas": 3, "targetCPUUtilizationPercentage": 65,
	}
	gateway["affinity"] = gatewayAffinity()
	globals := object(core, "global")
	globals["defaultPodDisruptionBudget"] = map[string]any{"enabled": true}
	controller := object(core, "controller")
	controller["replicas"] = 2
	controller["resources"] = map[string]any{
		"requests": map[string]any{"cpu": "500m", "memory": "1Gi"},
		"limits":   map[string]any{"cpu": "2", "memory": "2Gi"},
	}
	console := object(values, "higress-console")
	console["replicaCount"] = 2
	console["resources"] = map[string]any{
		"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
		"limits":   map[string]any{"cpu": "1", "memory": "1Gi"},
	}
	return nil
}

func upsertNewGroup(groups map[string]any, name string, zones, instanceTypes []any, minimum, desired, maximum, diskSize int, role, pool string) {
	group, exists := groups[name].(map[string]any)
	if !exists {
		group = map[string]any{}
		groups[name] = group
	}
	group["availability_zones"] = zones
	group["instance_types"] = instanceTypes
	group["capacity_type"] = "ON_DEMAND"
	group["subnet_type"] = "private"
	group["min_size"] = minimum
	group["desired_size"] = desired
	group["max_size"] = maximum
	group["capacity_deferred"] = false
	group["disk_size"] = diskSize
	group["labels"] = map[string]any{
		"workload-class":     role,
		"ops-deploy.io/pool": pool,
	}
	if role == "application" {
		group["taints"] = []any{map[string]any{
			"key": "workload-class", "value": "application", "effect": "NO_SCHEDULE",
		}}
	} else {
		group["taints"] = []any{}
	}
}

func gatewayAffinity() map[string]any {
	selector := map[string]any{"matchLabels": map[string]any{"app": "higress-gateway"}}
	return map[string]any{"podAntiAffinity": map[string]any{
		"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{
			"labelSelector": selector, "topologyKey": "kubernetes.io/hostname",
		}},
		"preferredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{
			"weight": 100,
			"podAffinityTerm": map[string]any{
				"labelSelector": selector, "topologyKey": "topology.kubernetes.io/zone",
			},
		}},
	}}
}

func object(parent map[string]any, key string) map[string]any {
	if value, ok := parent[key].(map[string]any); ok {
		return value
	}
	value := map[string]any{}
	parent[key] = value
	return value
}

func backupDocument(dir, target string, document environment.Document) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-before-node-isolation-%s.json", target, time.Now().UTC().Format("20060102T150405Z")))
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func printSummary(target access.ProjectEnvironment, document environment.Document, applied bool, backup string) {
	eks := object(document, "eks")
	groups := object(eks, "node_groups")
	fmt.Printf("project=%s environment=%s target=%s applied=%t node_groups=%d\n", target.ProjectKey, target.Environment, target.TargetName, applied, len(groups))
	for _, name := range []string{"application", "ops", "ingress-gateway", "business-workload", "platform-ops"} {
		if raw, exists := groups[name]; exists {
			group, _ := raw.(map[string]any)
			fmt.Printf("group=%s min=%v desired=%v max=%v instance_types=%v\n", name, group["min_size"], group["desired_size"], group["max_size"], group["instance_types"])
		}
	}
	if backup != "" {
		fmt.Printf("backup=%s\n", backup)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
