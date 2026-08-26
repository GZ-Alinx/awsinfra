package projectarchive

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Inventory struct {
	Projects []Project `yaml:"projects"`
}

type Project struct {
	Key          string        `yaml:"key"`
	DisplayName  string        `yaml:"display_name"`
	Environments []Environment `yaml:"environments"`
}

type Environment struct {
	Key     string `yaml:"key"`
	Context string `yaml:"context"`
}

type Release struct {
	Project      string    `yaml:"project" json:"-"`
	Environment  string    `yaml:"environment" json:"-"`
	Context      string    `yaml:"context" json:"-"`
	Name         string    `yaml:"name" json:"name"`
	Namespace    string    `yaml:"namespace" json:"namespace"`
	Revision     string    `yaml:"revision" json:"revision"`
	Updated      string    `yaml:"updated" json:"updated"`
	Status       string    `yaml:"status" json:"status"`
	Chart        string    `yaml:"chart" json:"chart"`
	ChartName    string    `yaml:"chart_name" json:"-"`
	ChartVersion string    `yaml:"chart_version" json:"-"`
	AppVersion   string    `yaml:"app_version" json:"app_version"`
	SyncedAt     time.Time `yaml:"synced_at" json:"-"`
}

type Service struct {
	Root        string
	Helm        string
	Kubectl     string
	Concurrency int
	Timeout     time.Duration
	Stdout      io.Writer
	Stderr      io.Writer
}

type helmEnvelope struct {
	Chart     *storedChart `json:"chart"`
	Config    any          `json:"config"`
	Manifest  string       `json:"manifest"`
	Name      string       `json:"name"`
	Namespace string       `json:"namespace"`
	Version   int          `json:"version"`
}

type storedChart struct {
	Metadata     map[string]any `json:"metadata"`
	Templates    []storedFile   `json:"templates"`
	Files        []storedFile   `json:"files"`
	Values       map[string]any `json:"values"`
	Schema       []byte         `json:"schema"`
	Lock         map[string]any `json:"lock"`
	Dependencies []*storedChart `json:"dependencies"`
}

type storedFile struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

type secretList struct {
	Items []struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	} `json:"items"`
}

var safeSegment = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
var chartVersionPattern = regexp.MustCompile(`^(.+)-([0-9][0-9A-Za-z.+_-]*)$`)
var privateKeyBlock = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z0-9 ]+ )?PRIVATE KEY-----`)
var forbiddenSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[A-Z0-9]{16}`),
	regexp.MustCompile(`ASIA[A-Z0-9]{16}`),
	regexp.MustCompile(`glpat-[A-Za-z0-9_.-]{15,}`),
	privateKeyBlock,
}

func LoadInventory(path string) (Inventory, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Inventory{}, err
	}
	var inventory Inventory
	if err := yaml.Unmarshal(payload, &inventory); err != nil {
		return Inventory{}, fmt.Errorf("解析归档清单: %w", err)
	}
	seen := map[string]bool{}
	for _, project := range inventory.Projects {
		if !safeSegment.MatchString(project.Key) {
			return Inventory{}, fmt.Errorf("项目标识不安全: %q", project.Key)
		}
		for _, environment := range project.Environments {
			if !safeSegment.MatchString(environment.Key) || strings.TrimSpace(environment.Context) == "" {
				return Inventory{}, fmt.Errorf("项目 %s 的环境配置不完整", project.Key)
			}
			key := project.Key + "/" + environment.Key
			if seen[key] {
				return Inventory{}, fmt.Errorf("环境重复: %s", key)
			}
			seen[key] = true
		}
	}
	return inventory, nil
}

func (s Service) Sync(ctx context.Context, inventory Inventory) error {
	s = s.defaults()
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return err
	}
	for _, project := range inventory.Projects {
		for _, environment := range project.Environments {
			if err := s.syncEnvironment(ctx, project, environment); err != nil {
				return fmt.Errorf("归档 %s/%s: %w", project.Key, environment.Key, err)
			}
		}
	}
	return nil
}

// CleanSnapshots removes generated observed snapshots while preserving every
// operator-owned working override. It is intentionally inventory-scoped so a
// typo in Root cannot remove unrelated platform data.
func (s Service) CleanSnapshots(inventory Inventory, confirmation string) error {
	s = s.defaults()
	if confirmation != "DELETE-GENERATED-SNAPSHOTS" {
		return errors.New("清理确认字符串不匹配")
	}
	base := filepath.Base(filepath.Clean(s.Root))
	if base != "project-archives" && base != "已部署项目归档" {
		return fmt.Errorf("拒绝清理非受管归档目录: %s", s.Root)
	}
	for _, project := range inventory.Projects {
		for _, environment := range project.Environments {
			root := filepath.Join(s.Root, project.Key, environment.Key)
			if err := os.Remove(filepath.Join(root, "current")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.RemoveAll(filepath.Join(root, "snapshots")); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s Service) syncEnvironment(ctx context.Context, project Project, environment Environment) error {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	environmentRoot := filepath.Join(s.Root, project.Key, environment.Key)
	temporary := filepath.Join(environmentRoot, "snapshots", "."+stamp+".tmp")
	snapshot := filepath.Join(environmentRoot, "snapshots", stamp)
	if err := os.RemoveAll(temporary); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(temporary, "helm"), 0o700); err != nil {
		return err
	}

	listPayload, err := s.run(ctx, nil, s.Helm, "--kube-context", environment.Context, "list", "--all-namespaces", "--output", "json")
	if err != nil {
		return err
	}
	var releases []Release
	if err := json.Unmarshal(listPayload, &releases); err != nil {
		return fmt.Errorf("解析 Helm release 列表: %w", err)
	}
	sort.Slice(releases, func(i, j int) bool {
		return releases[i].Namespace+"/"+releases[i].Name < releases[j].Namespace+"/"+releases[j].Name
	})

	jobs := make(chan Release)
	errCh := make(chan error, len(releases))
	var wait sync.WaitGroup
	for worker := 0; worker < s.Concurrency; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for release := range jobs {
				release.Project = project.Key
				release.Environment = environment.Key
				release.Context = environment.Context
				release.SyncedAt = time.Now().UTC()
				release.ChartName, release.ChartVersion = splitChart(release.Chart)
				if err := s.archiveRelease(ctx, temporary, release); err != nil {
					errCh <- fmt.Errorf("%s/%s: %w", release.Namespace, release.Name, err)
				}
			}
		}()
	}
	for _, release := range releases {
		jobs <- release
	}
	close(jobs)
	wait.Wait()
	close(errCh)
	var archiveErr error
	for err := range errCh {
		archiveErr = errors.Join(archiveErr, err)
	}
	if archiveErr != nil {
		_ = os.RemoveAll(temporary)
		return archiveErr
	}

	if err := s.archiveClusterResources(ctx, temporary, environment.Context); err != nil {
		_ = os.RemoveAll(temporary)
		return err
	}
	index := map[string]any{
		"project": project.Key, "display_name": project.DisplayName, "environment": environment.Key,
		"context": environment.Context, "synced_at": time.Now().UTC(), "release_count": len(releases),
	}
	if err := writeYAML(filepath.Join(temporary, "archive.yaml"), index); err != nil {
		return err
	}
	if err := auditSnapshot(temporary); err != nil {
		_ = os.RemoveAll(temporary)
		return err
	}
	if err := os.MkdirAll(filepath.Join(environmentRoot, "working", "helm"), 0o700); err != nil {
		return err
	}
	for _, release := range releases {
		override := filepath.Join(environmentRoot, "working", "helm", release.Namespace, release.Name, "values.override.yaml")
		if _, err := os.Stat(override); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(override), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(override, []byte("{}\n"), 0o600); err != nil {
				return err
			}
		}
	}
	if err := os.Rename(temporary, snapshot); err != nil {
		return err
	}
	current := filepath.Join(environmentRoot, "current")
	_ = os.Remove(current)
	if err := os.Symlink(filepath.Join("snapshots", stamp), current); err != nil {
		return err
	}
	if err := writeEnvironmentReadme(environmentRoot, s.Root, project, environment); err != nil {
		return err
	}
	fmt.Fprintf(s.Stdout, "已归档 %s/%s：%d 个 Helm release -> %s\n", project.Key, environment.Key, len(releases), snapshot)
	return nil
}

func (s Service) archiveRelease(ctx context.Context, snapshotRoot string, release Release) error {
	releaseRoot := filepath.Join(snapshotRoot, "helm", release.Namespace, release.Name)
	if err := os.MkdirAll(releaseRoot, 0o700); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(releaseRoot, "release.yaml"), release); err != nil {
		return err
	}
	values, err := s.run(ctx, nil, s.Helm, "--kube-context", release.Context, "get", "values", release.Name, "--namespace", release.Namespace, "--all", "--output", "yaml")
	if err != nil {
		return err
	}
	secretValues := collectSensitiveYAML(values)
	values = sanitizeYAML(values, false)
	values = redactKnownValues(values, secretValues)
	if err := os.WriteFile(filepath.Join(releaseRoot, "values.current.redacted.yaml"), values, 0o600); err != nil {
		return err
	}
	manifest, err := s.run(ctx, nil, s.Helm, "--kube-context", release.Context, "get", "manifest", release.Name, "--namespace", release.Namespace)
	if err != nil {
		return err
	}
	manifest = sanitizeYAML(manifest, true)
	manifest = redactKnownValues(manifest, secretValues)
	if err := os.WriteFile(filepath.Join(releaseRoot, "manifest.current.redacted.yaml"), manifest, 0o600); err != nil {
		return err
	}
	envelope, err := s.loadStoredRelease(ctx, release)
	if err != nil {
		return err
	}
	if envelope.Chart == nil {
		return errors.New("Helm release 中没有 Chart 数据")
	}
	return extractChart(filepath.Join(releaseRoot, "chart"), envelope.Chart)
}

func (s Service) loadStoredRelease(ctx context.Context, release Release) (helmEnvelope, error) {
	payload, err := s.run(ctx, nil, s.Kubectl, "--context", release.Context, "--namespace", release.Namespace,
		"get", "secrets", "--selector", "owner=helm,name="+release.Name, "--output", "json")
	if err != nil {
		return helmEnvelope{}, err
	}
	var list secretList
	if err := json.Unmarshal(payload, &list); err != nil {
		return helmEnvelope{}, err
	}
	bestVersion := -1
	encoded := ""
	for _, item := range list.Items {
		version, _ := strconv.Atoi(item.Metadata.Labels["version"])
		if version >= bestVersion && item.Data["release"] != "" {
			bestVersion = version
			encoded = item.Data["release"]
		}
	}
	if encoded == "" {
		return helmEnvelope{}, errors.New("找不到 Helm release 存储 Secret")
	}
	outer, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return helmEnvelope{}, err
	}
	compressed, err := base64.StdEncoding.DecodeString(string(outer))
	if err != nil {
		return helmEnvelope{}, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return helmEnvelope{}, err
	}
	defer reader.Close()
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return helmEnvelope{}, err
	}
	var envelope helmEnvelope
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		return helmEnvelope{}, err
	}
	return envelope, nil
}

func extractChart(root string, chart *storedChart) error {
	if chart == nil {
		return nil
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(root, "Chart.yaml"), chart.Metadata); err != nil {
		return err
	}
	if chart.Values != nil {
		if err := writeYAML(filepath.Join(root, "values.yaml"), chart.Values); err != nil {
			return err
		}
	}
	if len(chart.Schema) > 0 {
		if err := os.WriteFile(filepath.Join(root, "values.schema.json"), chart.Schema, 0o600); err != nil {
			return err
		}
	}
	if len(chart.Lock) > 0 {
		if err := writeYAML(filepath.Join(root, "Chart.lock"), chart.Lock); err != nil {
			return err
		}
	}
	for _, item := range append(append([]storedFile(nil), chart.Templates...), chart.Files...) {
		path, err := safeJoin(root, item.Name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, item.Data, 0o600); err != nil {
			return err
		}
	}
	for _, dependency := range chart.Dependencies {
		name := strings.TrimSpace(fmt.Sprint(dependency.Metadata["name"]))
		if !safeSegment.MatchString(name) {
			return fmt.Errorf("Chart dependency 名称不安全: %q", name)
		}
		if err := extractChart(filepath.Join(root, "charts", name), dependency); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) archiveClusterResources(ctx context.Context, snapshotRoot, kubeContext string) error {
	resources, err := s.run(ctx, nil, s.Kubectl, "--context", kubeContext, "get",
		"deployments,statefulsets,daemonsets,jobs,cronjobs,services,ingresses,horizontalpodautoscalers,poddisruptionbudgets",
		"--all-namespaces", "--output", "yaml")
	if err != nil {
		return err
	}
	resources = sanitizeYAML(resources, true)
	clusterRoot := filepath.Join(snapshotRoot, "cluster")
	if err := os.MkdirAll(clusterRoot, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(clusterRoot, "resources.current.redacted.yaml"), resources, 0o600); err != nil {
		return err
	}
	secrets, err := s.run(ctx, nil, s.Kubectl, "--context", kubeContext, "get", "secrets", "--all-namespaces",
		"--output", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,TYPE:.type", "--no-headers")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(clusterRoot, "secret-inventory.txt"), secrets, 0o600)
}

func (s Service) Plan(ctx context.Context, project, environment, namespace, release string) error {
	return s.change(ctx, project, environment, namespace, release, "", true)
}

func (s Service) Apply(ctx context.Context, project, environment, namespace, release, confirmation string) error {
	expected := project + "/" + environment + "/" + namespace + "/" + release
	if confirmation != expected {
		return fmt.Errorf("确认字符串不匹配；请输入 %s", expected)
	}
	return s.change(ctx, project, environment, namespace, release, confirmation, false)
}

func (s Service) change(ctx context.Context, project, environment, namespace, release, confirmation string, dryRun bool) error {
	s = s.defaults()
	for _, segment := range []string{project, environment, namespace, release} {
		if !safeSegment.MatchString(segment) {
			return fmt.Errorf("路径参数不安全: %q", segment)
		}
	}
	releaseRoot := filepath.Join(s.Root, project, environment, "current", "helm", namespace, release)
	metadataPayload, err := os.ReadFile(filepath.Join(releaseRoot, "release.yaml"))
	if err != nil {
		return fmt.Errorf("请先同步归档: %w", err)
	}
	var metadata Release
	if err := yaml.Unmarshal(metadataPayload, &metadata); err != nil {
		return err
	}
	override := filepath.Join(s.Root, project, environment, "working", "helm", namespace, release, "values.override.yaml")
	if _, err := os.Stat(override); err != nil {
		return err
	}
	chart := filepath.Join(releaseRoot, "chart")
	args := []string{"--kube-context", metadata.Context, "upgrade", release, chart, "--namespace", namespace,
		"--reuse-values", "--values", override, "--wait", "--timeout", "20m", "--history-max", "20"}
	if dryRun {
		args = append(args, "--dry-run=server", "--hide-secret")
		fmt.Fprintf(s.Stdout, "预检 %s/%s/%s/%s（Secret 已隐藏）\n", project, environment, namespace, release)
	} else {
		fmt.Fprintf(s.Stdout, "应用 %s/%s/%s/%s\n", project, environment, namespace, release)
	}
	_, err = s.run(ctx, s.Stdout, s.Helm, args...)
	return err
}

func (s Service) defaults() Service {
	if s.Root == "" {
		s.Root = "../已部署项目归档"
	}
	if s.Helm == "" {
		s.Helm = "helm"
	}
	if s.Kubectl == "" {
		s.Kubectl = "kubectl"
	}
	if s.Concurrency <= 0 {
		s.Concurrency = 4
	}
	if s.Timeout <= 0 {
		s.Timeout = 30 * time.Minute
	}
	if s.Stdout == nil {
		s.Stdout = os.Stdout
	}
	if s.Stderr == nil {
		s.Stderr = os.Stderr
	}
	return s
}

func (s Service) run(parent context.Context, stdout io.Writer, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, s.Timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...) // #nosec G204 -- explicit operator tools with validated arguments.
	var output bytes.Buffer
	command.Stdout = &output
	if stdout != nil {
		command.Stdout = io.MultiWriter(stdout, &output)
	}
	command.Stderr = s.Stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return output.Bytes(), nil
}

func splitChart(value string) (string, string) {
	match := chartVersionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 3 {
		return value, ""
	}
	return match[1], match[2]
}

func sanitizeYAML(payload []byte, stripRuntime bool) []byte {
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	for {
		var document any
		if err := decoder.Decode(&document); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return []byte("# 原始内容无法安全解析，未归档。\n")
		}
		document = sanitizeValue(document, stripRuntime)
		if document == nil {
			continue
		}
		_ = encoder.Encode(document)
	}
	_ = encoder.Close()
	return output.Bytes()
}

func sanitizeValue(value any, stripRuntime bool) any {
	switch item := value.(type) {
	case map[string]any:
		kind := strings.TrimSpace(fmt.Sprint(item["kind"]))
		if stripRuntime {
			delete(item, "status")
			if metadata, ok := item["metadata"].(map[string]any); ok {
				for _, key := range []string{"creationTimestamp", "generation", "managedFields", "resourceVersion", "selfLink", "uid"} {
					delete(metadata, key)
				}
			}
		}
		if strings.EqualFold(kind, "Secret") {
			if _, exists := item["data"]; exists {
				item["data"] = map[string]any{"__REDACTED__": ""}
			}
			if _, exists := item["stringData"]; exists {
				item["stringData"] = map[string]any{"__REDACTED__": ""}
			}
		}
		if name, ok := item["name"].(string); ok && sensitiveKey(name) {
			if _, exists := item["value"]; exists {
				item["value"] = "__REDACTED__"
			}
		}
		for key, nested := range item {
			if sensitiveKey(key) && !referenceKey(key) && isScalar(nested) {
				item[key] = "__REDACTED__"
				continue
			}
			item[key] = sanitizeValue(nested, stripRuntime)
		}
		return item
	case []any:
		for index := range item {
			item[index] = sanitizeValue(item[index], stripRuntime)
		}
		return item
	case string:
		return sanitizeEmbeddedText(item)
	default:
		return value
	}
}

func sensitiveKey(value string) bool {
	key := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(value))
	if strings.Contains(key, "serviceaccounttoken") || strings.Contains(key, "automounttoken") ||
		strings.Contains(key, "federatedtoken") || strings.Contains(key, "tokengeneration") {
		return false
	}
	for _, marker := range []string{"password", "passwd", "token", "privatekey", "secretkey", "secretaccesskey", "accesskeyid", "awsaccesskey", "clientsecret", "credential", "webhookurl", "apikey"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func referenceKey(value string) bool {
	key := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(value))
	for _, marker := range []string{"secretname", "existingsecret", "secretref", "secretkeyref", "credentialid", "passwordkey", "tokenkey"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func isScalar(value any) bool {
	switch value.(type) {
	case nil, string, bool, int, int64, float64:
		return true
	default:
		return false
	}
}

func sanitizeEmbeddedText(value string) string {
	if privateKeyBlock.MatchString(value) {
		value = privateKeyBlock.ReplaceAllString(value, "__REDACTED_PRIVATE_KEY__")
	}
	if !strings.ContainsAny(value, ":=") {
		return value
	}
	if strings.Contains(value, "\n") {
		var nested any
		if err := yaml.Unmarshal([]byte(value), &nested); err == nil {
			switch nested.(type) {
			case map[string]any, []any:
				if payload, err := yaml.Marshal(sanitizeValue(nested, false)); err == nil {
					return strings.TrimSuffix(string(payload), "\n")
				}
			}
		}
	}
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		separator := strings.IndexByte(line, ':')
		if equal := strings.IndexByte(line, '='); equal >= 0 && (separator < 0 || equal < separator) {
			separator = equal
		}
		if separator < 0 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(line[:separator]), "- '\"")
		if !sensitiveKey(key) || referenceKey(key) {
			continue
		}
		prefix := line[:separator+1]
		lines[index] = prefix + " __REDACTED__"
	}
	return strings.Join(lines, "\n")
}

func collectSensitiveYAML(payload []byte) []string {
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	unique := map[string]bool{}
	for {
		var document any
		if err := decoder.Decode(&document); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			break
		}
		collectSensitiveValues(document, unique)
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return len(result[i]) > len(result[j]) })
	return result
}

func collectSensitiveValues(value any, result map[string]bool) {
	switch item := value.(type) {
	case map[string]any:
		if name, ok := item["name"].(string); ok && sensitiveKey(name) {
			collectSensitiveScalar(item["value"], result)
		}
		for key, nested := range item {
			if sensitiveKey(key) && !referenceKey(key) {
				collectSensitiveScalar(nested, result)
			}
			collectSensitiveValues(nested, result)
		}
	case []any:
		for _, nested := range item {
			collectSensitiveValues(nested, result)
		}
	case string:
		if strings.Contains(item, "\n") {
			var nested any
			if err := yaml.Unmarshal([]byte(item), &nested); err == nil {
				collectSensitiveValues(nested, result)
			}
		}
	}
}

func collectSensitiveScalar(value any, result map[string]bool) {
	text, ok := value.(string)
	if !ok {
		return
	}
	text = strings.TrimSpace(text)
	if len(text) < 8 || strings.HasPrefix(text, "${") || strings.Contains(text, "__REDACTED__") {
		return
	}
	result[text] = true
}

func redactKnownValues(payload []byte, sensitive []string) []byte {
	for _, value := range sensitive {
		payload = bytes.ReplaceAll(payload, []byte(value), []byte("__REDACTED__"))
	}
	return payload
}

func auditSnapshot(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.Contains(entry.Name(), ".redacted.") {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, pattern := range forbiddenSecretPatterns {
			if pattern.Match(payload) {
				return fmt.Errorf("归档安全检查失败，脱敏文件仍包含高风险凭据特征: %s", path)
			}
		}
		if unredactedSensitiveYAML(payload) {
			return fmt.Errorf("归档安全检查失败，脱敏文件仍包含敏感字段明文: %s", path)
		}
		return nil
	})
}

func unredactedSensitiveYAML(payload []byte) bool {
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	for {
		var document any
		if err := decoder.Decode(&document); errors.Is(err, io.EOF) {
			return false
		} else if err != nil {
			return true
		}
		if containsUnredactedSensitiveValue(document) {
			return true
		}
	}
}

func containsUnredactedSensitiveValue(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		if name, ok := item["name"].(string); ok && sensitiveKey(name) {
			if text, ok := item["value"].(string); ok && text != "" && !strings.Contains(text, "__REDACTED__") {
				return true
			}
		}
		for key, nested := range item {
			if sensitiveKey(key) && !referenceKey(key) {
				if text, ok := nested.(string); ok && text != "" && !strings.Contains(text, "__REDACTED__") {
					return true
				}
			}
			if containsUnredactedSensitiveValue(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range item {
			if containsUnredactedSensitiveValue(nested) {
				return true
			}
		}
	case string:
		if strings.Contains(item, "\n") {
			var nested any
			if err := yaml.Unmarshal([]byte(item), &nested); err == nil {
				switch nested.(type) {
				case map[string]any, []any:
					return containsUnredactedSensitiveValue(nested)
				}
			}
		}
	}
	return false
}

func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean(strings.TrimPrefix(name, "/"))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("Chart 文件路径不安全: %q", name)
	}
	path := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return "", fmt.Errorf("Chart 文件越界: %q", name)
	}
	return path, nil
}

func writeYAML(path string, value any) error {
	payload, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o600)
}

func writeEnvironmentReadme(root, archiveRoot string, project Project, environment Environment) error {
	text := fmt.Sprintf(`# %s / %s 部署归档

- current/：最近一次从集群读取的不可变快照。
- snapshots/：历史快照，禁止手工修改。
- working/helm/<namespace>/<release>/values.override.yaml：后续 Helm 变更入口。

只编辑 override 文件。归档清单中的 Secret 和敏感字段已脱敏；升级时使用 Helm 当前 release values，避免密码被清空。

预检：

~~~console
go run ./cmd/project-manifest-archive plan --root %q --project %s --environment %s --namespace <namespace> --release <release>
~~~

应用时必须输入完整确认字符串：

~~~console
go run ./cmd/project-manifest-archive apply --root %q --project %s --environment %s --namespace <namespace> --release <release> --confirm %s/%s/<namespace>/<release>
~~~
`, project.DisplayName, environment.Key, archiveRoot, project.Key, environment.Key, archiveRoot, project.Key, environment.Key, project.Key, environment.Key)
	return os.WriteFile(filepath.Join(root, "README.md"), []byte(text), 0o600)
}
