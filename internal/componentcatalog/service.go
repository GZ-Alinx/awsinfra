package componentcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/sensitive"
)

var (
	ErrInvalidComponent  = errors.New("invalid Helm component definition")
	ErrHelmInspect       = errors.New("unable to read Helm chart values")
	ErrReservedComponent = errors.New("built-in component keys are reserved")
	ErrInlineSecret      = errors.New("inline secrets are not allowed in Helm values; use a Kubernetes Secret reference")
	keyPattern           = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	chartPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)
	versionPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]{0,127}$`)
	namespacePattern     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	valuePathPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
)

const maxValuesBytes = 1 << 20

type Component struct {
	Key              string         `json:"key"`
	DisplayName      string         `json:"display_name"`
	Category         string         `json:"category"`
	Description      string         `json:"description"`
	Repository       string         `json:"repository"`
	Chart            string         `json:"chart"`
	ChartVersion     string         `json:"chart_version"`
	DefaultNamespace string         `json:"default_namespace"`
	ReplicaPaths     []string       `json:"replica_paths"`
	ValuesYAML       string         `json:"values_yaml"`
	Values           map[string]any `json:"values"`
	CreatedBy        string         `json:"created_by"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type InspectRequest struct {
	Repository   string `json:"repository"`
	Chart        string `json:"chart"`
	ChartVersion string `json:"chart_version"`
}

type InspectResult struct {
	Repository             string         `json:"repository"`
	Chart                  string         `json:"chart"`
	ChartVersion           string         `json:"chart_version"`
	ValuesYAML             string         `json:"values_yaml"`
	Values                 map[string]any `json:"values"`
	FilteredSensitivePaths []string       `json:"filtered_sensitive_paths,omitempty"`
}

type VersionOption struct {
	Version     string `json:"version"`
	AppVersion  string `json:"app_version,omitempty"`
	Description string `json:"description,omitempty"`
}

type VersionResult struct {
	Repository string          `json:"repository"`
	Chart      string          `json:"chart"`
	Versions   []VersionOption `json:"versions"`
}

type Store interface {
	ListHelmComponents(context.Context) ([]Component, error)
	GetHelmComponent(context.Context, string) (Component, error)
	SaveHelmComponent(context.Context, Component) error
	DeleteHelmComponent(context.Context, string) error
}

type Service struct {
	store        Store
	helm         string
	allowPrivate bool
	reserved     map[string]bool
}

func New(config *appconfig.Config, store Store) *Service {
	reserved := make(map[string]bool, len(config.Components))
	for _, component := range config.Components {
		reserved[component.Key] = true
	}
	return &Service{
		store: store, helm: config.Tools.Helm,
		allowPrivate: config.Security.AllowPrivateHelmRepositories,
		reserved:     reserved,
	}
}

func (s *Service) List(ctx context.Context) ([]Component, error) {
	items, err := s.store.ListHelmComponents(ctx)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index] = withParsedValues(items[index])
	}
	return items, nil
}

func (s *Service) Get(ctx context.Context, key string) (Component, error) {
	component, err := s.store.GetHelmComponent(ctx, key)
	return withParsedValues(component), err
}

func (s *Service) Save(ctx context.Context, component Component) (Component, error) {
	component.Key = strings.TrimSpace(strings.ToLower(component.Key))
	component.DisplayName = strings.TrimSpace(component.DisplayName)
	component.Category = strings.TrimSpace(component.Category)
	component.Description = strings.TrimSpace(component.Description)
	component.Repository = strings.TrimSpace(component.Repository)
	component.Chart = strings.TrimSpace(component.Chart)
	component.ChartVersion = strings.TrimSpace(component.ChartVersion)
	component.DefaultNamespace = strings.TrimSpace(component.DefaultNamespace)
	component.ValuesYAML = strings.TrimSpace(component.ValuesYAML)
	for index := range component.ReplicaPaths {
		component.ReplicaPaths[index] = strings.TrimSpace(component.ReplicaPaths[index])
		if !valuePathPattern.MatchString(component.ReplicaPaths[index]) {
			return Component{}, ErrInvalidComponent
		}
	}
	if len(component.ReplicaPaths) > 20 {
		return Component{}, ErrInvalidComponent
	}
	if component.DefaultNamespace == "" {
		component.DefaultNamespace = "platform-server"
	}
	if s.reserved[component.Key] {
		return Component{}, ErrReservedComponent
	}
	if !validDefinition(component.Key, component.DisplayName, component.Repository, component.Chart, component.DefaultNamespace) {
		return Component{}, ErrInvalidComponent
	}
	if err := s.validateRepositoryHost(ctx, component.Repository); err != nil {
		return Component{}, ErrInvalidComponent
	}
	if len(component.ValuesYAML) > maxValuesBytes || (component.ChartVersion != "" && !versionPattern.MatchString(component.ChartVersion)) {
		return Component{}, ErrInvalidComponent
	}
	if component.ValuesYAML == "" {
		component.ValuesYAML = "{}\n"
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(component.ValuesYAML), &values); err != nil {
		return Component{}, fmt.Errorf("%w: values YAML is invalid", ErrInvalidComponent)
	}
	if sensitive.Has(values) {
		return Component{}, ErrInlineSecret
	}
	if err := s.store.SaveHelmComponent(ctx, component); err != nil {
		return Component{}, err
	}
	stored, err := s.store.GetHelmComponent(ctx, component.Key)
	return withParsedValues(stored), err
}

func (s *Service) Delete(ctx context.Context, key string) error {
	if !keyPattern.MatchString(key) {
		return ErrInvalidComponent
	}
	if s.reserved[key] {
		return ErrReservedComponent
	}
	return s.store.DeleteHelmComponent(ctx, key)
}

func (s *Service) Inspect(ctx context.Context, request InspectRequest) (InspectResult, error) {
	request.Repository = strings.TrimSpace(request.Repository)
	request.Chart = strings.TrimSpace(request.Chart)
	request.ChartVersion = strings.TrimSpace(request.ChartVersion)
	if !validRepository(request.Repository) || !validChart(request.Chart) ||
		(request.ChartVersion != "" && !versionPattern.MatchString(request.ChartVersion)) {
		return InspectResult{}, ErrInvalidComponent
	}
	commandCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	if err := s.validateRepositoryHost(commandCtx, request.Repository); err != nil {
		return InspectResult{}, ErrInvalidComponent
	}
	args := []string{"show", "values"}
	if strings.HasPrefix(request.Repository, "oci://") {
		args = append(args, strings.TrimSuffix(request.Repository, "/")+"/"+strings.TrimPrefix(request.Chart, "/"))
	} else {
		args = append(args, request.Chart, "--repo", request.Repository)
	}
	if request.ChartVersion != "" {
		args = append(args, "--version", request.ChartVersion)
	}
	cmd := exec.CommandContext(commandCtx, s.helm, args...) // #nosec G204 -- chart, version and repository are allowlist-validated and passed without a shell.
	helmHome, err := os.MkdirTemp("", "ops-deploy-helm-*")
	if err != nil {
		return InspectResult{}, ErrHelmInspect
	}
	defer os.RemoveAll(helmHome)
	for _, name := range []string{"config", "cache", "data"} {
		if err := os.Mkdir(filepath.Join(helmHome, name), 0o700); err != nil {
			return InspectResult{}, ErrHelmInspect
		}
	}
	cmd.Env = append(os.Environ(),
		"HELM_NO_PLUGINS=1",
		"HELM_CONFIG_HOME="+filepath.Join(helmHome, "config"),
		"HELM_CACHE_HOME="+filepath.Join(helmHome, "cache"),
		"HELM_DATA_HOME="+filepath.Join(helmHome, "data"),
	)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return InspectResult{}, ErrHelmInspect
	}
	payload, readErr := io.ReadAll(io.LimitReader(stdout, maxValuesBytes+1))
	if readErr != nil || len(payload) > maxValuesBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return InspectResult{}, ErrHelmInspect
	}
	if err := cmd.Wait(); err != nil {
		return InspectResult{}, ErrHelmInspect
	}
	values := map[string]any{}
	if err := yaml.Unmarshal(payload, &values); err != nil {
		return InspectResult{}, ErrHelmInspect
	}
	filtered := sensitive.Sanitize(values)
	sanitizedPayload, err := yaml.Marshal(values)
	if err != nil {
		return InspectResult{}, ErrHelmInspect
	}
	return InspectResult{
		Repository: request.Repository, Chart: request.Chart, ChartVersion: request.ChartVersion,
		ValuesYAML: string(sanitizedPayload), Values: values, FilteredSensitivePaths: filtered,
	}, nil
}

func (s *Service) Versions(ctx context.Context, request InspectRequest) (VersionResult, error) {
	request.Repository = strings.TrimSpace(request.Repository)
	request.Chart = strings.TrimSpace(request.Chart)
	request.ChartVersion = strings.TrimSpace(request.ChartVersion)
	if !validRepository(request.Repository) || !validChart(request.Chart) ||
		(request.ChartVersion != "" && !versionPattern.MatchString(request.ChartVersion)) {
		return VersionResult{}, ErrInvalidComponent
	}
	commandCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	if err := s.validateRepositoryHost(commandCtx, request.Repository); err != nil {
		return VersionResult{}, ErrInvalidComponent
	}
	// Helm cannot enumerate tags for every OCI registry. Keep the configured
	// version selectable and let HTTP chart repositories return their full index.
	if strings.HasPrefix(request.Repository, "oci://") {
		versions := []VersionOption{}
		if request.ChartVersion != "" {
			versions = append(versions, VersionOption{Version: request.ChartVersion})
		}
		return VersionResult{Repository: request.Repository, Chart: request.Chart, Versions: versions}, nil
	}
	helmHome, err := os.MkdirTemp("", "ops-deploy-helm-versions-*")
	if err != nil {
		return VersionResult{}, ErrHelmInspect
	}
	defer os.RemoveAll(helmHome)
	for _, name := range []string{"config", "cache", "data"} {
		if err := os.Mkdir(filepath.Join(helmHome, name), 0o700); err != nil {
			return VersionResult{}, ErrHelmInspect
		}
	}
	environment := append(os.Environ(),
		"HELM_NO_PLUGINS=1",
		"HELM_CONFIG_HOME="+filepath.Join(helmHome, "config"),
		"HELM_CACHE_HOME="+filepath.Join(helmHome, "cache"),
		"HELM_DATA_HOME="+filepath.Join(helmHome, "data"),
	)
	add := exec.CommandContext(commandCtx, s.helm, "repo", "add", "ops-deploy-version-catalog", request.Repository, "--force-update") // #nosec G204 -- validated arguments without a shell.
	add.Env = environment
	add.Stdout = io.Discard
	add.Stderr = io.Discard
	if err := add.Run(); err != nil {
		return VersionResult{}, ErrHelmInspect
	}
	search := exec.CommandContext(commandCtx, s.helm, "search", "repo", "ops-deploy-version-catalog/"+request.Chart, "--versions", "--output", "json") // #nosec G204 -- validated arguments without a shell.
	search.Env = environment
	search.Stderr = io.Discard
	payload, err := search.Output()
	if err != nil || len(payload) > maxValuesBytes {
		return VersionResult{}, ErrHelmInspect
	}
	var rows []struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		AppVersion  string `json:"app_version"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(payload, &rows); err != nil {
		return VersionResult{}, ErrHelmInspect
	}
	versions := make([]VersionOption, 0, min(len(rows), 100))
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Version == "" || seen[row.Version] {
			continue
		}
		seen[row.Version] = true
		versions = append(versions, VersionOption{Version: row.Version, AppVersion: row.AppVersion, Description: row.Description})
		if len(versions) == 100 {
			break
		}
	}
	if request.ChartVersion != "" && !seen[request.ChartVersion] {
		versions = append([]VersionOption{{Version: request.ChartVersion}}, versions...)
	}
	return VersionResult{Repository: request.Repository, Chart: request.Chart, Versions: versions}, nil
}

func validDefinition(key, displayName, repository, chart, namespace string) bool {
	return keyPattern.MatchString(key) && displayName != "" && len(displayName) <= 128 &&
		validRepository(repository) && validChart(chart) && len(namespace) <= 63 && namespacePattern.MatchString(namespace)
}

func withParsedValues(component Component) Component {
	component.Values = map[string]any{}
	_ = yaml.Unmarshal([]byte(component.ValuesYAML), &component.Values)
	sensitive.Sanitize(component.Values)
	if payload, err := yaml.Marshal(component.Values); err == nil {
		component.ValuesYAML = string(payload)
	}
	return component
}

func validRepository(value string) bool {
	if strings.HasPrefix(value, "oci://") {
		value = "https://" + strings.TrimPrefix(value, "oci://")
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" &&
		parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && len(value) <= 1000
}

func validChart(value string) bool {
	if !chartPattern.MatchString(value) || strings.Contains(value, "//") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (s *Service) validateRepositoryHost(ctx context.Context, repository string) error {
	value := repository
	if strings.HasPrefix(value, "oci://") {
		value = "https://" + strings.TrimPrefix(value, "oci://")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return ErrInvalidComponent
	}
	if parsed.Scheme != "https" && !s.allowPrivate {
		return ErrInvalidComponent
	}
	if s.allowPrivate {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return ErrInvalidComponent
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return ErrInvalidComponent
	}
	for _, address := range addresses {
		ip := address.IP
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsMulticast() || ip.IsUnspecified() {
			return ErrInvalidComponent
		}
	}
	return nil
}
