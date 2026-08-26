package kubetunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/cicd"
)

var (
	kubernetesNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	targetPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	regionPattern         = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-\d+$`)
	clusterPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)
	forwardPattern        = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+)`)
)

type AWSCredentialProvider interface {
	Environment(context.Context, string) ([]string, error)
}

type Manager struct {
	config   *appconfig.Config
	provider AWSCredentialProvider

	mu      sync.Mutex
	tunnels map[string]*tunnel
	closed  bool
}

type tunnel struct {
	endpoint string
	url      string
	cmd      *exec.Cmd
	alive    bool
}

func New(config *appconfig.Config, provider AWSCredentialProvider) *Manager {
	return &Manager{config: config, provider: provider, tunnels: make(map[string]*tunnel)}
}

func (m *Manager) Ensure(ctx context.Context, endpoint cicd.ManagedEndpoint) (string, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return "", err
	}
	key := endpoint.ProjectKey + "/" + endpoint.EnvironmentKey
	fingerprint := strings.Join([]string{endpoint.TargetName, endpoint.Region, endpoint.ClusterName, endpoint.Namespace, endpoint.ServiceName, strconv.Itoa(endpoint.ServicePort)}, "\x00")

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errors.New("Kubernetes tunnel manager is closed")
	}
	if current := m.tunnels[key]; current != nil && current.alive && current.endpoint == fingerprint {
		return current.url, nil
	}
	if current := m.tunnels[key]; current != nil {
		m.stopLocked(current)
		delete(m.tunnels, key)
	}
	kubeconfig, commandEnvironment, err := m.prepareKubeconfig(ctx, endpoint)
	if err != nil {
		return "", err
	}
	created, err := m.startLocked(ctx, key, fingerprint, kubeconfig, commandEnvironment, endpoint)
	if err != nil {
		return "", err
	}
	m.tunnels[key] = created
	return created.url, nil
}

func (m *Manager) prepareKubeconfig(ctx context.Context, endpoint cicd.ManagedEndpoint) (string, []string, error) {
	if m.provider == nil {
		return "", nil, errors.New("当前项目未绑定 AWS 凭据，无法建立 EKS 连接")
	}
	awsEnvironment, err := m.provider.Environment(ctx, endpoint.ProjectKey)
	if err != nil {
		return "", nil, fmt.Errorf("读取项目 AWS 凭据: %w", err)
	}
	if len(awsEnvironment) == 0 {
		return "", nil, errors.New("当前项目未绑定可用的 AWS 凭据，无法建立 EKS 连接")
	}
	commandEnvironment := removeEnvironmentKeys(os.Environ(), "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "KUBECONFIG")
	commandEnvironment = append(commandEnvironment, awsEnvironment...)
	dir := filepath.Join(m.config.Paths.DataDir, "kubeconfigs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	kubeconfig := filepath.Join(dir, endpoint.TargetName)
	commandCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, m.config.Tools.AWS,
		"eks", "update-kubeconfig", "--region", endpoint.Region, "--name", endpoint.ClusterName,
		"--alias", endpoint.ClusterName, "--kubeconfig", kubeconfig) // #nosec G204 -- executable is administrator-owned and all dynamic values are allowlist validated.
	cmd.Dir, cmd.Env = m.config.Paths.RepositoryRoot, commandEnvironment
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("生成 EKS kubeconfig: %w: %s", err, limitOutput(output))
	}
	if err := os.Chmod(kubeconfig, 0o600); err != nil {
		return "", nil, err
	}
	return kubeconfig, append(commandEnvironment, "KUBECONFIG="+kubeconfig), nil
}

func (m *Manager) startLocked(ctx context.Context, key, fingerprint, kubeconfig string, commandEnvironment []string, endpoint cicd.ManagedEndpoint) (*tunnel, error) {
	cmd := exec.Command(m.config.Tools.Kubectl, "--kubeconfig", kubeconfig, "-n", endpoint.Namespace,
		"port-forward", "service/"+endpoint.ServiceName, ":"+strconv.Itoa(endpoint.ServicePort), "--address", "127.0.0.1") // #nosec G204 -- executable is administrator-owned and Kubernetes names are allowlist validated.
	cmd.Dir, cmd.Env = m.config.Paths.RepositoryRoot, commandEnvironment
	configureTunnelProcess(cmd)
	reader, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("start kubectl port-forward: %w", err)
	}
	created := &tunnel{endpoint: fingerprint, cmd: cmd, alive: true}
	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			default:
			}
		}
		close(lines)
	}()
	go func() {
		_ = cmd.Wait()
		_ = writer.Close()
		_ = reader.Close()
		m.mu.Lock()
		created.alive = false
		if m.tunnels[key] == created {
			delete(m.tunnels, key)
		}
		m.mu.Unlock()
	}()

	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	var diagnostics []string
	for {
		select {
		case <-ctx.Done():
			m.stopLocked(created)
			return nil, ctx.Err()
		case <-timer.C:
			m.stopLocked(created)
			return nil, fmt.Errorf("kubectl port-forward 启动超时: %s", strings.Join(diagnostics, "; "))
		case line, ok := <-lines:
			if !ok {
				m.stopLocked(created)
				return nil, fmt.Errorf("kubectl port-forward 提前退出: %s", strings.Join(diagnostics, "; "))
			}
			if len(diagnostics) < 8 {
				diagnostics = append(diagnostics, line)
			}
			match := forwardPattern.FindStringSubmatch(line)
			if len(match) != 2 {
				continue
			}
			port, err := strconv.Atoi(match[1])
			if err != nil || port < 1 || port > 65535 {
				continue
			}
			created.url = "http://127.0.0.1:" + strconv.Itoa(port)
			return created, nil
		}
	}
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for key, current := range m.tunnels {
		m.stopLocked(current)
		delete(m.tunnels, key)
	}
	return nil
}

func (m *Manager) stopLocked(current *tunnel) {
	if current == nil || current.cmd == nil || current.cmd.Process == nil || !current.alive {
		return
	}
	current.alive = false
	if err := terminateTunnelProcess(current.cmd); err != nil {
		_ = current.cmd.Process.Kill()
	}
}

func validateEndpoint(endpoint cicd.ManagedEndpoint) error {
	if endpoint.ProjectKey == "" || endpoint.EnvironmentKey == "" || !targetPattern.MatchString(endpoint.TargetName) ||
		!regionPattern.MatchString(endpoint.Region) || !clusterPattern.MatchString(endpoint.ClusterName) ||
		!kubernetesNamePattern.MatchString(endpoint.Namespace) || !kubernetesNamePattern.MatchString(endpoint.ServiceName) ||
		endpoint.ServicePort < 1 || endpoint.ServicePort > 65535 {
		return errors.New("invalid managed Jenkins EKS endpoint")
	}
	return nil
}

func removeEnvironmentKeys(values []string, keys ...string) []string {
	blocked := make(map[string]bool, len(keys))
	for _, key := range keys {
		blocked[key] = true
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		if !blocked[key] {
			result = append(result, value)
		}
	}
	return result
}

func limitOutput(value []byte) string {
	text := strings.TrimSpace(string(value))
	if len(text) > 2000 {
		text = text[len(text)-2000:]
	}
	return text
}
