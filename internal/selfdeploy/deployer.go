package selfdeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
)

var imageTagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

type Deployer struct {
	config       *Config
	out          io.Writer
	errOut       io.Writer
	dockerConfig string
}

type callerIdentity struct {
	Account string `json:"Account"`
	Arn     string `json:"Arn"`
}

type clusterDescription struct {
	Cluster struct {
		Status    string `json:"status"`
		Version   string `json:"version"`
		Endpoint  string `json:"endpoint"`
		Resources struct {
			EndpointPublicAccess  bool `json:"endpointPublicAccess"`
			EndpointPrivateAccess bool `json:"endpointPrivateAccess"`
		} `json:"resourcesVpcConfig"`
	} `json:"cluster"`
}

const platformDeployLockName = "ops-deploy-platform-release-lock"

type platformDeployLock struct {
	Data map[string]string `json:"data"`
}

type manifestData struct {
	Namespace            string
	StorageClass         string
	PlatformStorage      string
	MySQLStorage         string
	RedisStorage         string
	ServiceType          string
	ExternalOrigin       string
	CookieSecure         string
	Image                string
	IngressEnabled       bool
	IngressClassName     string
	IngressHost          string
	IngressTLSSecretName string
}

func New(config *Config, out, errOut io.Writer) *Deployer {
	return &Deployer{config: config, out: out, errOut: errOut}
}

func (d *Deployer) Preflight(ctx context.Context, requireDocker bool) (string, error) {
	tools := []string{"aws", "kubectl"}
	if requireDocker {
		tools = append(tools, "docker")
	}
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return "", fmt.Errorf("required local tool %s is unavailable", tool)
		}
	}
	identity, cluster, err := d.connect(ctx)
	if err != nil {
		return "", err
	}
	if requireDocker {
		if _, err := d.run(ctx, nil, true, "docker", "buildx", "version"); err != nil {
			return "", errors.New("Docker buildx is required for multi-platform image builds")
		}
	}
	secrets, err := loadSecrets(d.config.Kubernetes.SecretsFile)
	if err != nil {
		return "", fmt.Errorf("validate deployment secrets before building the image: %w", err)
	}
	for key := range secrets {
		secrets[key] = ""
	}
	if _, err := d.kubectl(ctx, nil, true, "get", "storageclass", d.config.Kubernetes.StorageClass); err != nil {
		return "", fmt.Errorf("required StorageClass %s is unavailable: %w", d.config.Kubernetes.StorageClass, err)
	}
	permissionOutput, err := d.kubectl(ctx, nil, false, "auth", "can-i", "create", "deployments.apps", "--namespace", d.config.Kubernetes.Namespace)
	if err != nil || strings.TrimSpace(string(permissionOutput)) != "yes" {
		return "", errors.New("current Kubernetes identity cannot create Deployments in the target namespace")
	}
	if d.config.Kubernetes.Ingress.Enabled {
		permissionOutput, err = d.kubectl(ctx, nil, false, "auth", "can-i", "create", "secrets", "--namespace", d.config.Kubernetes.Namespace)
		if err != nil || strings.TrimSpace(string(permissionOutput)) != "yes" {
			return "", errors.New("current Kubernetes identity cannot create the TLS Secret in the target namespace")
		}
		permissionOutput, err = d.kubectl(ctx, nil, false, "auth", "can-i", "get", "secret/"+d.config.Kubernetes.Ingress.TLSSecretName, "--namespace", d.config.Kubernetes.Ingress.TLSSecretSourceNamespace)
		if err != nil || strings.TrimSpace(string(permissionOutput)) != "yes" {
			return "", errors.New("current Kubernetes identity cannot read the configured Higress TLS Secret")
		}
		tlsSecret, err := d.ingressTLSSecretDocument(ctx)
		if err != nil {
			return "", err
		}
		clear(tlsSecret)
		if err := d.validateIngressHostOwnership(ctx); err != nil {
			return "", err
		}
		if _, err := d.gatewayAddress(ctx); err != nil {
			return "", err
		}
	}
	fmt.Fprintf(d.out, "预检通过：AWS=%s EKS=%s Kubernetes=%s public=%t private=%t\n", identity.Arn, d.config.Cluster.Name, cluster.Cluster.Version, cluster.Cluster.Resources.EndpointPublicAccess, cluster.Cluster.Resources.EndpointPrivateAccess)
	return identity.Account, nil
}

func (d *Deployer) Deploy(ctx context.Context, tag string, skipBuild bool) error {
	if !skipBuild {
		cleanupDockerConfig, err := d.useEphemeralDockerConfig()
		if err != nil {
			return err
		}
		defer cleanupDockerConfig()
	}
	account, err := d.Preflight(ctx, !skipBuild)
	if err != nil {
		return err
	}
	releaseLock, err := d.acquireDeployLock(ctx)
	if err != nil {
		return err
	}
	defer releaseLock()
	if tag = strings.TrimSpace(tag); tag == "" {
		tag = strings.TrimSpace(d.config.Registry.Tag)
	}
	if tag == "" {
		if skipBuild {
			return errors.New("--skip-build requires an existing image tag through --tag or registry.tag")
		}
		tag = "build-" + time.Now().UTC().Format("20060102T150405Z")
	}
	if !imageTagPattern.MatchString(tag) {
		return errors.New("image tag contains unsupported characters")
	}
	registry := fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com", account, d.config.Cluster.Region)
	image := registry + "/" + d.config.Registry.Repository + ":" + tag
	if err := d.ensureRepository(ctx); err != nil {
		return err
	}
	tagExists, err := d.imageTagExists(ctx, tag)
	if err != nil {
		return err
	}
	if err := validateImageTagAvailability(tag, tagExists, skipBuild); err != nil {
		return err
	}
	if !skipBuild {
		fmt.Fprintf(d.out, "构建镜像：%s\n", image)
		if _, err := d.run(ctx, nil, true, "docker", "buildx", "build", "--platform", d.config.Build.Platform, "--file", d.config.Build.Dockerfile, "--tag", image, "--load", d.config.Build.Context); err != nil {
			return err
		}
		password, err := d.runAWS(ctx, nil, false, "ecr", "get-login-password", "--region", d.config.Cluster.Region)
		if err != nil {
			return err
		}
		password = append(bytes.TrimSpace(password), '\n')
		if _, err := d.run(ctx, password, false, "docker", "login", "--username", "AWS", "--password-stdin", registry); err != nil {
			clear(password)
			return err
		}
		clear(password)
		if _, err := d.run(ctx, nil, true, "docker", "push", image); err != nil {
			return err
		}
	}
	if err := d.applyNamespace(ctx); err != nil {
		return err
	}
	if d.config.Kubernetes.Ingress.Enabled {
		tlsSecret, err := d.ingressTLSSecretDocument(ctx)
		if err != nil {
			return err
		}
		if _, err := d.kubectl(ctx, tlsSecret, true, "apply", "-f", "-"); err != nil {
			clear(tlsSecret)
			return err
		}
		clear(tlsSecret)
	}
	secrets, err := loadSecrets(d.config.Kubernetes.SecretsFile)
	if err != nil {
		return err
	}
	secretPayload, err := secretDocument(d.config.Kubernetes.Namespace, secrets)
	for key := range secrets {
		secrets[key] = ""
	}
	if err != nil {
		return err
	}
	if _, err := d.kubectl(ctx, secretPayload, true, "apply", "-f", "-"); err != nil {
		clear(secretPayload)
		return err
	}
	clear(secretPayload)
	manifest, err := d.RenderManifest(image)
	if err != nil {
		return err
	}
	if _, err := d.kubectl(ctx, manifest, true, "apply", "-f", "-"); err != nil {
		return err
	}
	if d.config.Kubernetes.Ingress.Enabled {
		if err := d.syncManagedIngressAddress(ctx); err != nil {
			return err
		}
	}
	for _, workload := range []string{"statefulset/ops-deploy-mysql", "statefulset/ops-deploy-redis"} {
		if err := d.waitForRollout(ctx, workload, "15m"); err != nil {
			return err
		}
	}
	if err := d.waitForRollout(ctx, "deployment/ops-deploy-platform", "20m"); err != nil {
		return err
	}
	if err := d.StatusConnected(ctx); err != nil {
		return err
	}
	fmt.Fprintf(d.out, "部署完成：image=%s namespace=%s\n", image, d.config.Kubernetes.Namespace)
	return nil
}

func (d *Deployer) acquireDeployLock(ctx context.Context) (func(), error) {
	host, _ := os.Hostname()
	holder := fmt.Sprintf("%s-%d-%d", defaultLockHolder(host), os.Getpid(), time.Now().UTC().UnixNano())
	created := time.Now().UTC().Format(time.RFC3339)
	create := func() error {
		_, err := d.kubectl(ctx, nil, false,
			"create", "configmap", platformDeployLockName,
			"--namespace", d.config.Kubernetes.Namespace,
			"--from-literal=holder="+holder,
			"--from-literal=created_at="+created,
		)
		return err
	}
	if err := create(); err != nil {
		payload, getErr := d.kubectl(ctx, nil, false, "get", "configmap/"+platformDeployLockName, "--namespace", d.config.Kubernetes.Namespace, "-o", "json")
		if getErr != nil {
			return nil, fmt.Errorf("平台发布锁创建失败且无法读取现有锁: %w", err)
		}
		var existing platformDeployLock
		if json.Unmarshal(payload, &existing) != nil {
			return nil, fmt.Errorf("平台已有发布任务，当前发布已停止；请等待任务结束后重试")
		}
		existingCreated, parseErr := time.Parse(time.RFC3339, existing.Data["created_at"])
		if parseErr != nil || time.Since(existingCreated) < 4*time.Hour {
			return nil, fmt.Errorf("平台已有发布任务正在执行（holder=%s，started=%s）；为防止新旧镜像互相覆盖，本次发布未启动", existing.Data["holder"], existing.Data["created_at"])
		}
		if _, deleteErr := d.kubectl(ctx, nil, false, "delete", "configmap/"+platformDeployLockName, "--namespace", d.config.Kubernetes.Namespace, "--ignore-not-found=true"); deleteErr != nil {
			return nil, fmt.Errorf("清理超过 4 小时的过期平台发布锁失败: %w", deleteErr)
		}
		if err := create(); err != nil {
			return nil, fmt.Errorf("重新获取平台发布锁失败，可能有另一任务刚刚启动: %w", err)
		}
	}
	fmt.Fprintf(d.out, "已获取平台发布锁：%s\n", holder)
	return func() {
		releaseContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		payload, err := d.kubectl(releaseContext, nil, false, "get", "configmap/"+platformDeployLockName, "--namespace", d.config.Kubernetes.Namespace, "-o", "json")
		if err != nil {
			return
		}
		var current platformDeployLock
		if json.Unmarshal(payload, &current) != nil || current.Data["holder"] != holder {
			return
		}
		_, _ = d.kubectl(releaseContext, nil, false, "delete", "configmap/"+platformDeployLockName, "--namespace", d.config.Kubernetes.Namespace, "--ignore-not-found=true")
	}, nil
}

func defaultLockHolder(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "unknown-host"
}

func validateImageTagAvailability(tag string, exists, skipBuild bool) error {
	switch {
	case exists && !skipBuild:
		return fmt.Errorf("ECR 镜像 Tag %q 已存在且仓库禁止覆盖；请使用新的不可变 Tag，或确认复用旧镜像后添加 --skip-build", tag)
	case !exists && skipBuild:
		return fmt.Errorf("ECR 镜像 Tag %q 不存在，--skip-build 不能部署尚未推送的镜像", tag)
	default:
		return nil
	}
}

func (d *Deployer) Status(ctx context.Context) error {
	if _, _, err := d.connect(ctx); err != nil {
		return err
	}
	return d.StatusConnected(ctx)
}

func (d *Deployer) StatusConnected(ctx context.Context) error {
	if _, err := d.kubectl(ctx, nil, true, "get", "deployments,statefulsets,pods,pvc,services,ingress", "--namespace", d.config.Kubernetes.Namespace, "-o", "wide"); err != nil {
		return err
	}
	return d.verifyPlatformHealth(ctx)
}

func (d *Deployer) waitForRollout(ctx context.Context, workload, timeout string) error {
	if _, err := d.kubectl(ctx, nil, true, "rollout", "status", workload, "--namespace", d.config.Kubernetes.Namespace, "--timeout="+timeout); err != nil {
		fmt.Fprintf(d.errOut, "\n%s 发布失败，开始收集 Pod、事件和最近日志：\n", workload)
		d.emitWorkloadDiagnostics(ctx, workload)
		return fmt.Errorf("%s 未在 %s 内就绪；上方诊断信息包含 Pod 状态、事件和最近日志，可修复后使用新 Tag 重试，或执行 make platform-rollback: %w", workload, timeout, err)
	}
	return nil
}

func (d *Deployer) emitWorkloadDiagnostics(ctx context.Context, workload string) {
	name := strings.TrimPrefix(strings.TrimPrefix(workload, "deployment/"), "statefulset/")
	selector := "app.kubernetes.io/name=" + name
	_, _ = d.kubectl(ctx, nil, true, "get", "pods", "--namespace", d.config.Kubernetes.Namespace, "--selector", selector, "-o", "wide")
	_, _ = d.kubectl(ctx, nil, true, "describe", workload, "--namespace", d.config.Kubernetes.Namespace)
	_, _ = d.kubectl(ctx, nil, true, "get", "events", "--namespace", d.config.Kubernetes.Namespace, "--sort-by=.lastTimestamp")
	_, _ = d.kubectl(ctx, nil, true, "logs", "--namespace", d.config.Kubernetes.Namespace, "--selector", selector, "--all-containers=true", "--tail=120", "--prefix=true")
}

func (d *Deployer) Rollback(ctx context.Context) error {
	if _, _, err := d.connect(ctx); err != nil {
		return err
	}
	if _, err := d.kubectl(ctx, nil, true, "rollout", "undo", "deployment/ops-deploy-platform", "--namespace", d.config.Kubernetes.Namespace); err != nil {
		return err
	}
	_, err := d.kubectl(ctx, nil, true, "rollout", "status", "deployment/ops-deploy-platform", "--namespace", d.config.Kubernetes.Namespace, "--timeout=20m")
	return err
}

func (d *Deployer) RenderManifest(image string) ([]byte, error) {
	tmpl, err := template.New(filepath.Base(d.config.Kubernetes.ManifestTemplate)).Funcs(template.FuncMap{"q": strconv.Quote}).ParseFiles(d.config.Kubernetes.ManifestTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse Kubernetes manifest template: %w", err)
	}
	data := manifestData{
		Namespace: d.config.Kubernetes.Namespace, StorageClass: d.config.Kubernetes.StorageClass,
		PlatformStorage: d.config.Kubernetes.PlatformStorage, MySQLStorage: d.config.Kubernetes.MySQLStorage,
		RedisStorage: d.config.Kubernetes.RedisStorage, ServiceType: d.config.Kubernetes.ServiceType,
		ExternalOrigin: d.config.Kubernetes.ExternalOrigin, CookieSecure: strconv.FormatBool(d.config.Kubernetes.CookieSecure),
		Image: image, IngressEnabled: d.config.Kubernetes.Ingress.Enabled,
		IngressClassName: d.config.Kubernetes.Ingress.ClassName, IngressHost: d.config.Kubernetes.Ingress.Host,
		IngressTLSSecretName: d.config.Kubernetes.Ingress.TLSSecretName,
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return nil, fmt.Errorf("render Kubernetes manifest: %w", err)
	}
	return output.Bytes(), nil
}

func (d *Deployer) connect(ctx context.Context) (callerIdentity, clusterDescription, error) {
	var identity callerIdentity
	// Pin STS to the deployment Region. Besides reducing latency, this avoids
	// making every release depend on the legacy global sts.amazonaws.com DNS
	// record when the regional AWS endpoints are healthy.
	identityPayload, err := d.runAWS(ctx, nil, false,
		"sts", "get-caller-identity", "--region", d.config.Cluster.Region, "--output", "json")
	if err != nil {
		return identity, clusterDescription{}, fmt.Errorf("verify AWS identity: %w", err)
	}
	if err := json.Unmarshal(identityPayload, &identity); err != nil || identity.Account == "" || identity.Arn == "" {
		return identity, clusterDescription{}, errors.New("AWS returned an invalid caller identity")
	}
	var cluster clusterDescription
	clusterPayload, err := d.runAWS(ctx, nil, false, "eks", "describe-cluster", "--name", d.config.Cluster.Name, "--region", d.config.Cluster.Region, "--output", "json")
	if err != nil {
		return identity, cluster, fmt.Errorf("describe target EKS cluster: %w", err)
	}
	if err := json.Unmarshal(clusterPayload, &cluster); err != nil {
		return identity, cluster, fmt.Errorf("parse EKS response: %w", err)
	}
	if cluster.Cluster.Status != "ACTIVE" {
		return identity, cluster, fmt.Errorf("target EKS cluster is %s, expected ACTIVE", cluster.Cluster.Status)
	}
	if err := os.MkdirAll(filepath.Dir(d.config.Cluster.Kubeconfig), 0o700); err != nil {
		return identity, cluster, err
	}
	if _, err := d.runAWS(ctx, nil, true, "eks", "update-kubeconfig", "--name", d.config.Cluster.Name, "--region", d.config.Cluster.Region, "--kubeconfig", d.config.Cluster.Kubeconfig, "--alias", d.config.Cluster.ContextAlias); err != nil {
		return identity, cluster, err
	}
	if err := d.stabilizeKubeconfigEndpoint(ctx, cluster.Cluster.Endpoint); err != nil {
		return identity, cluster, err
	}
	if err := os.Chmod(d.config.Cluster.Kubeconfig, 0o600); err != nil {
		return identity, cluster, fmt.Errorf("secure local kubeconfig: %w", err)
	}
	if _, err := d.kubectl(ctx, nil, false, "get", "--raw=/readyz"); err != nil {
		return identity, cluster, fmt.Errorf("target Kubernetes API is not reachable: %w", err)
	}
	return identity, cluster, nil
}

func (d *Deployer) ensureRepository(ctx context.Context) error {
	output, err := d.runAWS(ctx, nil, false, "ecr", "describe-repositories", "--repository-names", d.config.Registry.Repository, "--region", d.config.Cluster.Region)
	if err == nil {
		return nil
	}
	if !strings.Contains(string(output), "RepositoryNotFoundException") {
		return fmt.Errorf("query ECR repository %s: %w", d.config.Registry.Repository, err)
	}
	if !d.config.Registry.CreateRepository {
		return fmt.Errorf("ECR repository %s does not exist and automatic creation is disabled", d.config.Registry.Repository)
	}
	_, err = d.runAWS(ctx, nil, true, "ecr", "create-repository", "--repository-name", d.config.Registry.Repository, "--region", d.config.Cluster.Region, "--image-tag-mutability", "IMMUTABLE", "--image-scanning-configuration", "scanOnPush=true", "--encryption-configuration", "encryptionType=AES256")
	return err
}

func (d *Deployer) imageTagExists(ctx context.Context, tag string) (bool, error) {
	output, err := d.runAWS(ctx, nil, false,
		"ecr", "describe-images",
		"--repository-name", d.config.Registry.Repository,
		"--image-ids", "imageTag="+tag,
		"--region", d.config.Cluster.Region,
		"--query", "imageDetails[0].imageDigest",
		"--output", "text",
	)
	if err != nil {
		if strings.Contains(string(output), "ImageNotFoundException") {
			return false, nil
		}
		return false, fmt.Errorf("query ECR image tag %s: %w", tag, err)
	}
	value := strings.TrimSpace(string(output))
	return value != "" && !strings.EqualFold(value, "none"), nil
}

func (d *Deployer) applyNamespace(ctx context.Context) error {
	payload, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": d.config.Kubernetes.Namespace, "labels": map[string]string{"app.kubernetes.io/part-of": "ops-deploy-platform"}},
	})
	if err != nil {
		return err
	}
	_, err = d.kubectl(ctx, payload, true, "apply", "-f", "-")
	return err
}

func (d *Deployer) runAWS(ctx context.Context, stdin []byte, visible bool, args ...string) ([]byte, error) {
	if d.config.Cluster.Profile != "" {
		args = append([]string{"--profile", d.config.Cluster.Profile}, args...)
	}
	return d.run(ctx, stdin, visible, "aws", args...)
}

func (d *Deployer) kubectl(ctx context.Context, stdin []byte, visible bool, args ...string) ([]byte, error) {
	prefix := []string{"--kubeconfig", d.config.Cluster.Kubeconfig, "--context", d.config.Cluster.ContextAlias}
	return d.run(ctx, stdin, visible, "kubectl", append(prefix, args...)...)
}

func (d *Deployer) run(ctx context.Context, stdin []byte, visible bool, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...) // #nosec G204 -- executable and arguments are validated platform configuration.
	if name == "docker" && d.dockerConfig != "" {
		command.Env = append(os.Environ(), "DOCKER_CONFIG="+d.dockerConfig)
	}
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var output bytes.Buffer
	if visible {
		command.Stdout = io.MultiWriter(d.out, &output)
		command.Stderr = io.MultiWriter(d.errOut, &output)
	} else {
		command.Stdout = &output
		command.Stderr = &output
	}
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			message = err.Error()
		}
		return output.Bytes(), fmt.Errorf("%s failed: %s", name, message)
	}
	return output.Bytes(), nil
}

func (d *Deployer) useEphemeralDockerConfig() (func(), error) {
	directory, err := os.MkdirTemp("", "ops-deploy-docker-")
	if err != nil {
		return nil, fmt.Errorf("create temporary Docker configuration: %w", err)
	}
	cleanup := func() {
		d.dockerConfig = ""
		_ = os.RemoveAll(directory)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte("{\n  \"auths\": {}\n}\n"), 0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("write temporary Docker configuration: %w", err)
	}
	pluginDirectory := filepath.Join(directory, "cli-plugins")
	if err := os.Mkdir(pluginDirectory, 0o700); err != nil {
		cleanup()
		return nil, err
	}
	if source := findBuildxPlugin(); source != "" {
		if err := os.Symlink(source, filepath.Join(pluginDirectory, "docker-buildx")); err != nil {
			cleanup()
			return nil, fmt.Errorf("expose Docker buildx to temporary configuration: %w", err)
		}
	}
	d.dockerConfig = directory
	return cleanup, nil
}

func findBuildxPlugin() string {
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".docker", "cli-plugins", "docker-buildx"))
	}
	candidates = append(candidates,
		"/Applications/Docker.app/Contents/Resources/cli-plugins/docker-buildx",
		"/opt/homebrew/lib/docker/cli-plugins/docker-buildx",
		"/usr/local/lib/docker/cli-plugins/docker-buildx",
		"/usr/libexec/docker/cli-plugins/docker-buildx",
	)
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}
