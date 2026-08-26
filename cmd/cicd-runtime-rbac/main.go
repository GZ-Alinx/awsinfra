package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/awscredentials"
	"ops-deploy-platform/internal/persistence"
)

func main() {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "", "project key")
	connectionKey := flag.String("connection", "", "managed Jenkins connection key")
	namespaces := flag.String("namespaces", "", "comma-separated deployment namespaces")
	flag.Parse()
	if *project == "" || *connectionKey == "" || len(split(*namespaces)) == 0 {
		fatal(fmt.Errorf("project, connection and namespaces are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	awsService, err := awscredentials.New(config, store)
	if err != nil {
		fatal(err)
	}
	connection, err := store.GetCICDConnection(ctx, *project, *connectionKey)
	if err != nil {
		fatal(err)
	}
	if connection.ConnectionMode != "eks_port_forward" || connection.Region == "" || connection.ClusterName == "" {
		fatal(fmt.Errorf("connection is not bound to a managed EKS cluster"))
	}
	awsEnv, err := awsService.Environment(ctx, *project)
	if err != nil {
		fatal(err)
	}
	runtimeEnv := append(withoutAWS(os.Environ()), awsEnv...)
	runtimeEnv = append(runtimeEnv, "AWS_REGION="+connection.Region, "AWS_DEFAULT_REGION="+connection.Region)
	dir, err := os.MkdirTemp("", "ops-cicd-rbac-")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(dir)
	kubeconfig := filepath.Join(dir, "config")
	if output, err := run(ctx, runtimeEnv, nil, config.Tools.AWS, "eks", "update-kubeconfig", "--name", connection.ClusterName, "--region", connection.Region, "--kubeconfig", kubeconfig, "--alias", "ops-cicd-rbac", "--no-cli-pager"); err != nil {
		fatal(fmt.Errorf("prepare kubeconfig: %w: %s", err, strings.TrimSpace(output)))
	}
	serviceAccount := "jenkins-" + strings.ToLower(strings.TrimSpace(*project))
	for _, namespace := range split(*namespaces) {
		manifest := runtimeRBAC(namespace, serviceAccount)
		if output, err := run(ctx, append(runtimeEnv, "KUBECONFIG="+kubeconfig), []byte(manifest), config.Tools.Kubectl, "apply", "-f", "-"); err != nil {
			fatal(fmt.Errorf("apply runtime RBAC for %s: %w: %s", namespace, err, strings.TrimSpace(output)))
		}
		for _, check := range [][]string{{"auth", "can-i", "get", "serviceaccounts", "-n", namespace, "--as=system:serviceaccount:platform-server:" + serviceAccount}, {"auth", "can-i", "patch", "deployments.apps", "-n", namespace, "--as=system:serviceaccount:platform-server:" + serviceAccount}} {
			output, err := run(ctx, append(runtimeEnv, "KUBECONFIG="+kubeconfig), nil, config.Tools.Kubectl, check...)
			if err != nil || strings.TrimSpace(output) != "yes" {
				fatal(fmt.Errorf("RBAC verification failed for %s: %s", namespace, strings.TrimSpace(output)))
			}
		}
		fmt.Printf("namespace=%s service_account=%s status=ready\n", namespace, serviceAccount)
	}
}

func runtimeRBAC(namespace, serviceAccount string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: platform-server
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ops-deploy-jenkins
  namespace: %s
rules:
  - apiGroups: [""]
    resources: ["services", "configmaps", "secrets", "serviceaccounts", "persistentvolumeclaims", "pods", "pods/log"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["apps"]
    resources: ["deployments", "deployments/status", "statefulsets", "statefulsets/status", "daemonsets", "daemonsets/status", "replicasets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["batch"]
    resources: ["jobs", "cronjobs"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["autoscaling"]
    resources: ["horizontalpodautoscalers"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses", "networkpolicies"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["policy"]
    resources: ["poddisruptionbudgets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ops-deploy-jenkins
  namespace: %s
subjects:
  - kind: ServiceAccount
    name: %s
    namespace: platform-server
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ops-deploy-jenkins
`, namespace, serviceAccount, namespace, namespace, serviceAccount)
}

func split(value string) []string {
	items := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func withoutAWS(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, item := range environment {
		if !strings.HasPrefix(item, "AWS_") {
			result = append(result, item)
		}
	}
	return result
}

func run(ctx context.Context, environment []string, stdin []byte, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = environment
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
