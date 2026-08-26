package status

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ops-deploy-platform/internal/environment"

	"gopkg.in/yaml.v3"
)

const maxIngressYAMLBytes = 256 * 1024

var kubernetesObjectNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type KubernetesIngress struct {
	Name              string                  `json:"name"`
	Namespace         string                  `json:"namespace"`
	ClassName         string                  `json:"class_name"`
	ResourceVersion   string                  `json:"resource_version"`
	Hosts             []string                `json:"hosts"`
	Paths             []KubernetesIngressPath `json:"paths"`
	TLSSecrets        []string                `json:"tls_secrets"`
	Addresses         []string                `json:"addresses"`
	CreationTimestamp string                  `json:"creation_timestamp,omitempty"`
	ManagedBy         string                  `json:"managed_by,omitempty"`
	BackendProtocol   string                  `json:"backend_protocol,omitempty"`
	SyncStatus        string                  `json:"sync_status"`
	Desired           bool                    `json:"desired"`
	ConfigIndex       *int                    `json:"config_index,omitempty"`
}

type KubernetesIngressPath struct {
	Host             string `json:"host,omitempty"`
	Path             string `json:"path"`
	PathType         string `json:"path_type,omitempty"`
	ServiceName      string `json:"service_name"`
	ServiceNamespace string `json:"service_namespace,omitempty"`
	ServicePort      string `json:"service_port"`
}

type KubernetesIngressDocument struct {
	Ingress KubernetesIngress `json:"ingress"`
	YAML    string            `json:"yaml"`
}

type KubernetesIngressValidation struct {
	Valid          bool              `json:"valid"`
	Ingress        KubernetesIngress `json:"ingress"`
	NormalizedYAML string            `json:"normalized_yaml"`
	Warnings       []string          `json:"warnings"`
}

type IngressConfigSyncReport struct {
	UpdatedDomains      int      `json:"updated_domains"`
	ImportedDomains     int      `json:"imported_domains"`
	ImportedRoutes      int      `json:"imported_routes"`
	ConsolidatedDomains int      `json:"consolidated_domains"`
	PreservedDomains    int      `json:"preserved_domains"`
	Skipped             []string `json:"skipped"`
}

func (s *Service) ListIngresses(ctx context.Context, name string) ([]KubernetesIngress, error) {
	items, err := loadOperationalValue(ctx, s.operations, operationalCacheKey(name, "ingresses"), 10*time.Second, func() ([]KubernetesIngress, error) {
		commandContext, doc, kubeconfig, contextErr := s.ingressKubernetesContext(ctx, name)
		if contextErr != nil {
			return nil, contextErr
		}
		defer os.Remove(kubeconfig)
		namespaces := ingressAllowedNamespaces(doc)
		result := make([]KubernetesIngress, 0)
		for _, namespace := range namespaces {
			payload, captureErr := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{
				"get", "ingresses.networking.k8s.io", "--namespace", namespace, "--output", "json",
			}, kubeconfig)
			if captureErr != nil {
				return nil, fmt.Errorf("读取 Namespace %s 的 Ingress 失败，请检查 Kubernetes RBAC 权限: %w", namespace, captureErr)
			}
			namespaceItems, decodeErr := decodeKubernetesIngressList(payload)
			if decodeErr != nil {
				return nil, errors.New("EKS 返回的 Ingress 数据格式无效")
			}
			result = append(result, namespaceItems...)
		}
		result = reconcileConfiguredIngresses(doc, result)
		sort.Slice(result, func(i, j int) bool {
			if result[i].Namespace == result[j].Namespace {
				return result[i].Name < result[j].Name
			}
			return result[i].Namespace < result[j].Namespace
		})
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	return cloneKubernetesIngresses(items), nil
}

func (s *Service) GetIngress(ctx context.Context, targetName, namespace, ingressName string) (KubernetesIngressDocument, error) {
	commandContext, doc, kubeconfig, err := s.ingressKubernetesContext(ctx, targetName)
	if err != nil {
		return KubernetesIngressDocument{}, err
	}
	defer os.Remove(kubeconfig)
	if err := validateIngressIdentity(doc, namespace, ingressName); err != nil {
		return KubernetesIngressDocument{}, err
	}
	payload, err := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{
		"get", "ingresses.networking.k8s.io", ingressName, "--namespace", namespace, "--output", "json",
	}, kubeconfig)
	if err != nil {
		return KubernetesIngressDocument{}, fmt.Errorf("读取 Ingress %s/%s 失败: %w", namespace, ingressName, err)
	}
	return editableIngressDocument(payload)
}

func (s *Service) ValidateIngress(ctx context.Context, targetName string, source []byte) (KubernetesIngressValidation, error) {
	commandContext, doc, kubeconfig, err := s.ingressKubernetesContext(ctx, targetName)
	if err != nil {
		return KubernetesIngressValidation{}, err
	}
	defer os.Remove(kubeconfig)
	normalized, summary, warnings, err := normalizeIngressYAML(doc, source)
	if err != nil {
		return KubernetesIngressValidation{}, err
	}
	if err := s.validateIngressBackends(commandContext, kubeconfig, summary); err != nil {
		return KubernetesIngressValidation{}, err
	}
	if _, err := s.captureIngressInput(commandContext, s.config.Tools.Kubectl, []string{
		"apply", "--dry-run=server", "--filename", "-",
	}, kubeconfig, normalized); err != nil {
		return KubernetesIngressValidation{}, fmt.Errorf("Kubernetes 服务端校验未通过: %w", err)
	}
	return KubernetesIngressValidation{
		Valid: true, Ingress: summary, NormalizedYAML: string(normalized), Warnings: warnings,
	}, nil
}

func (s *Service) ApplyIngress(ctx context.Context, targetName string, source []byte, expectedNamespace, expectedName, expectedResourceVersion string) (KubernetesIngressDocument, error) {
	commandContext, doc, kubeconfig, err := s.ingressKubernetesContext(ctx, targetName)
	if err != nil {
		return KubernetesIngressDocument{}, err
	}
	defer os.Remove(kubeconfig)
	normalized, summary, _, err := normalizeIngressYAML(doc, source)
	if err != nil {
		return KubernetesIngressDocument{}, err
	}
	if index, managed := configuredIngressIndex(doc, summary.Namespace, summary.Name); managed {
		return KubernetesIngressDocument{}, fmt.Errorf(
			"Ingress %s/%s 由部署配置中的域名规则 %d 管理；请在“部署配置 → 域名转发”中修改，防止下次阶段二覆盖手工变更",
			summary.Namespace, summary.Name, index+1,
		)
	}
	if expectedNamespace != "" && (summary.Namespace != strings.TrimSpace(expectedNamespace) || summary.Name != strings.TrimSpace(expectedName)) {
		return KubernetesIngressDocument{}, errors.New("编辑时不能修改 Ingress 的 Namespace 或名称；如需改名，请创建新对象并删除旧对象")
	}
	if err := s.validateIngressBackends(commandContext, kubeconfig, summary); err != nil {
		return KubernetesIngressDocument{}, err
	}
	currentVersion, exists, err := s.currentIngressResourceVersion(commandContext, kubeconfig, summary.Namespace, summary.Name)
	if err != nil {
		return KubernetesIngressDocument{}, err
	}
	expectedResourceVersion = strings.TrimSpace(expectedResourceVersion)
	switch {
	case expectedResourceVersion == "" && exists:
		return KubernetesIngressDocument{}, fmt.Errorf("Ingress %s/%s 已存在，请刷新列表后进入编辑，平台不会覆盖未知对象", summary.Namespace, summary.Name)
	case expectedResourceVersion != "" && !exists:
		return KubernetesIngressDocument{}, fmt.Errorf("Ingress %s/%s 已被其他操作删除，请刷新列表", summary.Namespace, summary.Name)
	case expectedResourceVersion != "" && currentVersion != expectedResourceVersion:
		return KubernetesIngressDocument{}, fmt.Errorf("Ingress %s/%s 已被其他用户或部署任务更新，请刷新后重新编辑", summary.Namespace, summary.Name)
	}
	if _, err := s.captureIngressInput(commandContext, s.config.Tools.Kubectl, []string{
		"apply", "--dry-run=server", "--filename", "-",
	}, kubeconfig, normalized); err != nil {
		return KubernetesIngressDocument{}, fmt.Errorf("Kubernetes 服务端校验未通过: %w", err)
	}
	if _, err := s.captureIngressInput(commandContext, s.config.Tools.Kubectl, []string{
		"apply", "--filename", "-",
	}, kubeconfig, normalized); err != nil {
		return KubernetesIngressDocument{}, fmt.Errorf("应用 Ingress %s/%s 失败: %w", summary.Namespace, summary.Name, err)
	}
	payload, err := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{
		"get", "ingresses.networking.k8s.io", summary.Name, "--namespace", summary.Namespace, "--output", "json",
	}, kubeconfig)
	if err != nil {
		return KubernetesIngressDocument{}, fmt.Errorf("Ingress 已应用，但回读结果失败: %w", err)
	}
	document, err := editableIngressDocument(payload)
	if err == nil {
		s.operations.delete(targetName, "ingresses")
	}
	return document, err
}

func (s *Service) DeleteIngress(ctx context.Context, targetName, namespace, ingressName, expectedResourceVersion string) error {
	commandContext, doc, kubeconfig, err := s.ingressKubernetesContext(ctx, targetName)
	if err != nil {
		return err
	}
	defer os.Remove(kubeconfig)
	if err := validateIngressIdentity(doc, namespace, ingressName); err != nil {
		return err
	}
	if index, managed := configuredIngressIndex(doc, namespace, ingressName); managed {
		return fmt.Errorf(
			"Ingress %s/%s 由部署配置中的域名规则 %d 管理；请先从“部署配置 → 域名转发”移除并执行阶段二，平台已阻止直接删除造成配置漂移",
			namespace, ingressName, index+1,
		)
	}
	currentVersion, exists, err := s.currentIngressResourceVersion(commandContext, kubeconfig, namespace, ingressName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("Ingress %s/%s 不存在或已经删除", namespace, ingressName)
	}
	if expected := strings.TrimSpace(expectedResourceVersion); expected != "" && expected != currentVersion {
		return fmt.Errorf("Ingress %s/%s 已被其他用户或部署任务更新，请刷新后再删除", namespace, ingressName)
	}
	if _, err := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{
		"delete", "ingresses.networking.k8s.io", ingressName, "--namespace", namespace, "--wait=true", "--timeout=30s",
	}, kubeconfig); err != nil {
		return fmt.Errorf("删除 Ingress %s/%s 失败: %w", namespace, ingressName, err)
	}
	s.operations.delete(targetName, "ingresses")
	return nil
}

func (s *Service) ingressKubernetesContext(ctx context.Context, name string) (context.Context, environment.Document, string, error) {
	commandContext, doc, kubeconfig, err := s.kubernetesContext(ctx, name)
	if err != nil {
		return ctx, nil, "", err
	}
	if len(ingressAllowedNamespaces(doc)) == 0 {
		_ = os.Remove(kubeconfig)
		return ctx, nil, "", errors.New("当前环境没有配置可管理的 Namespace")
	}
	return commandContext, doc, kubeconfig, nil
}

func cloneKubernetesIngresses(source []KubernetesIngress) []KubernetesIngress {
	result := append([]KubernetesIngress(nil), source...)
	for index := range result {
		result[index].Hosts = append([]string(nil), source[index].Hosts...)
		result[index].Paths = append([]KubernetesIngressPath(nil), source[index].Paths...)
		result[index].TLSSecrets = append([]string(nil), source[index].TLSSecrets...)
		result[index].Addresses = append([]string(nil), source[index].Addresses...)
	}
	return result
}

// reconcileConfiguredIngresses overlays the environment's desired domain
// routes on top of the live cluster inventory. The deployment configuration is
// the single source of truth; the Ingress page uses these states to explain
// whether phase two still needs to run or whether the live object drifted.
func reconcileConfiguredIngresses(doc environment.Document, actual []KubernetesIngress) []KubernetesIngress {
	expected := configuredIngresses(doc)
	actualByIdentity := make(map[string]int, len(actual))
	for index := range actual {
		actual[index].SyncStatus = "cluster-only"
		actual[index].Desired = false
		actualByIdentity[ingressIdentity(actual[index].Namespace, actual[index].Name)] = index
	}
	expectedCounts := make(map[string]int, len(expected))
	for _, desired := range expected {
		expectedCounts[ingressIdentity(desired.Namespace, desired.Name)]++
	}
	handledConflicts := make(map[string]struct{})
	for _, desired := range expected {
		identity := ingressIdentity(desired.Namespace, desired.Name)
		if expectedCounts[identity] > 1 {
			if _, handled := handledConflicts[identity]; handled {
				continue
			}
			handledConflicts[identity] = struct{}{}
			if index, exists := actualByIdentity[identity]; exists {
				live := &actual[index]
				live.Desired = true
				live.ConfigIndex = desired.ConfigIndex
				live.ManagedBy = "deployment-config"
				live.SyncStatus = "conflict"
			} else {
				desired.SyncStatus = "conflict"
				desired.Desired = true
				desired.ManagedBy = "deployment-config"
				actual = append(actual, desired)
			}
			continue
		}
		if index, exists := actualByIdentity[identity]; exists {
			live := &actual[index]
			live.Desired = true
			live.ConfigIndex = desired.ConfigIndex
			live.ManagedBy = "deployment-config"
			if ingressRoutingEqual(*live, desired) {
				live.SyncStatus = "synced"
			} else {
				live.SyncStatus = "drifted"
			}
			continue
		}
		desired.SyncStatus = "pending"
		desired.Desired = true
		desired.ManagedBy = "deployment-config"
		actual = append(actual, desired)
	}
	return actual
}

func configuredIngresses(doc environment.Document) []KubernetesIngress {
	rawDomains, _ := doc["domains"].([]any)
	certificateSecrets := configuredTLSSecrets(doc)
	result := make([]KubernetesIngress, 0, len(rawDomains))
	for index, raw := range rawDomains {
		domain := ingressMap(raw)
		if len(domain) == 0 || !ingressBoolDefault(domain["enabled"], true) {
			continue
		}
		protocol := strings.ToLower(ingressString(domain["protocol"]))
		if protocol == "" {
			if ingressBoolDefault(domain["tls_enabled"], false) {
				protocol = "https"
			} else {
				protocol = "http"
			}
		}
		if protocol == "tcp" {
			continue
		}
		namespace := ingressString(domain["namespace"])
		if namespace == "" {
			namespace = "platform-server"
		}
		name := configuredIngressName(domain, index)
		if name == "" {
			continue
		}
		configIndex := index
		item := KubernetesIngress{
			Name: name, Namespace: namespace, ClassName: ingressString(domain["gateway"]),
			Hosts: make([]string, 0), Paths: make([]KubernetesIngressPath, 0),
			TLSSecrets: make([]string, 0), Addresses: make([]string, 0),
			BackendProtocol: strings.ToLower(ingressString(domain["backend_protocol"])),
			Desired:         true, ConfigIndex: &configIndex,
		}
		if item.ClassName == "" {
			item.ClassName = "higress"
		}
		host := ingressString(domain["domain"])
		if ingressString(domain["access_type"]) != "ip" && host != "" {
			item.Hosts = []string{host}
		} else {
			host = ""
		}
		rawRoutes, ok := domain["routes"].([]any)
		if !ok || len(rawRoutes) == 0 {
			rawRoutes = []any{domain}
		}
		for _, rawRoute := range rawRoutes {
			route := ingressMap(rawRoute)
			serviceName := ingressString(route["service"])
			servicePort := ingressPortString(route["service_port"])
			if serviceName == "" || servicePort == "" {
				continue
			}
			path := ingressString(route["path"])
			if path == "" {
				path = "/"
			}
			pathType := ingressString(route["path_type"])
			if pathType == "" {
				pathType = "Prefix"
			}
			item.Paths = append(item.Paths, KubernetesIngressPath{
				Host: host, Path: path, PathType: pathType, ServiceName: serviceName,
				ServiceNamespace: namespace, ServicePort: servicePort,
			})
		}
		if ingressBoolDefault(domain["tls_enabled"], false) && host != "" {
			secretName := certificateSecrets[ingressString(domain["certificate_ref"])]
			if secretName == "" {
				secretName = ingressString(domain["tls_secret_name"])
			}
			if secretName == "" {
				secretName = strings.ReplaceAll(host, ".", "-") + "-tls"
			}
			item.TLSSecrets = []string{secretName}
		}
		result = append(result, item)
	}
	return result
}

func configuredTLSSecrets(doc environment.Document) map[string]string {
	result := make(map[string]string)
	tlsConfig := ingressMap(doc["tls"])
	for _, raw := range ingressSlice(tlsConfig["certificates"]) {
		certificate := ingressMap(raw)
		key, secret := ingressString(certificate["key"]), ingressString(certificate["tls_secret_name"])
		if key != "" && secret != "" {
			result[key] = secret
		}
	}
	return result
}

func configuredIngressName(domain map[string]any, index int) string {
	if name := ingressString(domain["name"]); name != "" {
		return name
	}
	if ingressString(domain["access_type"]) == "ip" {
		return "ip-route-" + strconv.Itoa(index)
	}
	return strings.ReplaceAll(ingressString(domain["domain"]), ".", "-")
}

func configuredIngressIndex(doc environment.Document, namespace, name string) (int, bool) {
	for _, item := range configuredIngresses(doc) {
		if item.Namespace == strings.TrimSpace(namespace) && item.Name == strings.TrimSpace(name) && item.ConfigIndex != nil {
			return *item.ConfigIndex, true
		}
	}
	return 0, false
}

func ingressRoutingEqual(actual, desired KubernetesIngress) bool {
	if actual.Namespace != desired.Namespace || actual.Name != desired.Name || actual.ClassName != desired.ClassName {
		return false
	}
	if normalizeIngressBackendProtocol(actual.BackendProtocol) != normalizeIngressBackendProtocol(desired.BackendProtocol) {
		return false
	}
	return ingressStringSetEqual(actual.Hosts, desired.Hosts) &&
		ingressStringSetEqual(actual.TLSSecrets, desired.TLSSecrets) &&
		ingressPathSetEqual(actual.Paths, desired.Paths)
}

func normalizeIngressBackendProtocol(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "http"
	}
	return value
}

func ingressStringSetEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	return strings.Join(leftCopy, "\x00") == strings.Join(rightCopy, "\x00")
}

func ingressPathSetEqual(left, right []KubernetesIngressPath) bool {
	if len(left) != len(right) {
		return false
	}
	signature := func(path KubernetesIngressPath) string {
		pathType := strings.TrimSpace(path.PathType)
		if pathType == "" {
			pathType = "Prefix"
		}
		pathValue := strings.TrimSpace(path.Path)
		if pathValue == "" {
			pathValue = "/"
		}
		return strings.Join([]string{path.Host, pathValue, pathType, path.ServiceName, path.ServicePort}, "\x00")
	}
	leftValues, rightValues := make([]string, 0, len(left)), make([]string, 0, len(right))
	for _, path := range left {
		leftValues = append(leftValues, signature(path))
	}
	for _, path := range right {
		rightValues = append(rightValues, signature(path))
	}
	sort.Strings(leftValues)
	sort.Strings(rightValues)
	return strings.Join(leftValues, "\x01") == strings.Join(rightValues, "\x01")
}

func ingressIdentity(namespace, name string) string {
	return strings.TrimSpace(namespace) + "/" + strings.TrimSpace(name)
}

type ingressDomainReference struct {
	index  int
	domain map[string]any
}

type ingressDomainRoute struct {
	path             string
	pathType         string
	service          string
	serviceNamespace string
	servicePort      int
}

// SyncIngressesToDomainConfig imports only route data that can be mapped back
// safely to an existing project domain. Routes are grouped by host because one
// domain is commonly split into several Higress objects. The side with more
// valid, non-conflicting paths wins; equal counts preserve deployment config.
func SyncIngressesToDomainConfig(doc environment.Document, inventory []KubernetesIngress) IngressConfigSyncReport {
	return syncIngressesToDomainConfig(doc, inventory, false)
}

// SyncIngressesToDomainConfigFromCluster treats EKS as authoritative for every
// registered host and imports new, unambiguous Ingress hosts from namespaces
// owned by this project environment. It never removes desired routes that have
// not reached the cluster yet.
func SyncIngressesToDomainConfigFromCluster(doc environment.Document, inventory []KubernetesIngress) IngressConfigSyncReport {
	return syncIngressesToDomainConfig(doc, inventory, true)
}

func syncIngressesToDomainConfig(doc environment.Document, inventory []KubernetesIngress, clusterAuthoritative bool) IngressConfigSyncReport {
	report := IngressConfigSyncReport{Skipped: make([]string, 0)}
	rawDomains, ok := doc["domains"].([]any)
	if !ok {
		if !clusterAuthoritative {
			return report
		}
		rawDomains = make([]any, 0)
		doc["domains"] = rawDomains
	}
	if len(rawDomains) == 0 && !clusterAuthoritative {
		return report
	}
	configuredByHost := make(map[string][]ingressDomainReference)
	for index, raw := range rawDomains {
		domain := ingressMap(raw)
		if len(domain) == 0 || !ingressBoolDefault(domain["enabled"], true) ||
			strings.EqualFold(ingressString(domain["protocol"]), "tcp") ||
			strings.EqualFold(ingressString(domain["access_type"]), "ip") {
			continue
		}
		host := strings.ToLower(ingressString(domain["domain"]))
		if host != "" {
			configuredByHost[host] = append(configuredByHost[host], ingressDomainReference{index: index, domain: domain})
		}
	}
	actualByHost := make(map[string][]ingressDomainRoute)
	actualClasses := make(map[string]map[string]struct{})
	actualTLSSecrets := make(map[string]map[string]struct{})
	actualBackendProtocols := make(map[string]map[string]struct{})
	actualIngressNames := make(map[string]map[string]struct{})
	for _, item := range inventory {
		if strings.TrimSpace(item.ResourceVersion) == "" {
			continue
		}
		for _, path := range item.Paths {
			host := strings.ToLower(strings.TrimSpace(path.Host))
			if host == "" && len(item.Hosts) == 1 {
				host = strings.ToLower(strings.TrimSpace(item.Hosts[0]))
			}
			if host == "" {
				continue
			}
			if _, configured := configuredByHost[host]; !configured &&
				(!clusterAuthoritative || !configuredIngressNamespace(doc, item.Namespace)) {
				continue
			}
			port, err := strconv.Atoi(strings.TrimSpace(path.ServicePort))
			if err != nil || port < 1 || port > 65535 || strings.TrimSpace(path.ServiceName) == "" {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s/%s 的路径 %s 使用了无法回填的 Service 端口", item.Namespace, item.Name, path.Path))
				continue
			}
			namespace := strings.TrimSpace(path.ServiceNamespace)
			if namespace == "" {
				namespace = item.Namespace
			}
			actualByHost[host] = append(actualByHost[host], ingressDomainRoute{
				path: defaultIngressPath(path.Path), pathType: defaultIngressPathType(path.PathType),
				service: strings.TrimSpace(path.ServiceName), serviceNamespace: namespace, servicePort: port,
			})
			if actualClasses[host] == nil {
				actualClasses[host] = make(map[string]struct{})
			}
			if className := strings.ToLower(strings.TrimSpace(item.ClassName)); className != "" {
				actualClasses[host][className] = struct{}{}
			}
			if actualIngressNames[host] == nil {
				actualIngressNames[host] = make(map[string]struct{})
			}
			if name := strings.TrimSpace(item.Name); name != "" {
				actualIngressNames[host][name] = struct{}{}
			}
			if actualBackendProtocols[host] == nil {
				actualBackendProtocols[host] = make(map[string]struct{})
			}
			if protocol := strings.ToLower(strings.TrimSpace(item.BackendProtocol)); protocol != "" {
				actualBackendProtocols[host][protocol] = struct{}{}
			}
			if actualTLSSecrets[host] == nil {
				actualTLSSecrets[host] = make(map[string]struct{})
			}
			for _, secret := range item.TLSSecrets {
				if secret = strings.TrimSpace(secret); secret != "" {
					actualTLSSecrets[host][secret] = struct{}{}
				}
			}
		}
	}

	removeIndexes := make(map[int]struct{})
	hosts := make([]string, 0, len(configuredByHost))
	for host := range configuredByHost {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		references := configuredByHost[host]
		sort.Slice(references, func(i, j int) bool { return references[i].index < references[j].index })
		desiredRoutes, desiredNamespace, desiredOK := configuredDomainRouteSet(references)
		if !desiredOK {
			report.Skipped = append(report.Skipped, fmt.Sprintf("%s 的部署配置包含相同路径但后端不同，未自动合并", host))
			continue
		}
		actualRoutes, actualNamespace, actualOK := uniqueIngressDomainRoutes(actualByHost[host])
		actualWins := actualOK && len(actualRoutes) > 0 &&
			(len(actualRoutes) > len(desiredRoutes) ||
				(clusterAuthoritative && !ingressDomainRoutesEqual(actualRoutes, desiredRoutes)))
		if !actualWins && len(references) == 1 {
			report.PreservedDomains++
			continue
		}
		if len(references) > 1 && !compatibleDomainReferences(references) {
			report.Skipped = append(report.Skipped, fmt.Sprintf("%s 的多条配置包含不同网关、TLS 或注解，未自动合并", host))
			continue
		}
		chosenRoutes, chosenNamespace := desiredRoutes, desiredNamespace
		if actualWins {
			chosenRoutes, chosenNamespace = actualRoutes, actualNamespace
			gateway := strings.ToLower(ingressString(references[0].domain["gateway"]))
			if classes := actualClasses[host]; len(classes) > 1 || (len(classes) == 1 && gateway != "" && !setContains(classes, gateway)) {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s 在 EKS 中同时使用了不同网关，未自动回填", host))
				continue
			}
		}
		if chosenNamespace == "" || !configuredIngressNamespace(doc, chosenNamespace) {
			report.Skipped = append(report.Skipped, fmt.Sprintf("%s 的后端 Namespace %s 不属于当前项目环境", host, chosenNamespace))
			continue
		}
		if len(chosenRoutes) == 0 || len(chosenRoutes) > 64 {
			report.Skipped = append(report.Skipped, fmt.Sprintf("%s 的有效路由数量不在 1 到 64 范围内", host))
			continue
		}
		primary := references[0].domain
		primary["namespace"] = chosenNamespace
		primary["routes"] = ingressDomainRouteDocuments(chosenRoutes)
		primary["path"] = chosenRoutes[0].path
		primary["path_type"] = chosenRoutes[0].pathType
		primary["service"] = chosenRoutes[0].service
		primary["service_port"] = chosenRoutes[0].servicePort
		for _, duplicate := range references[1:] {
			removeIndexes[duplicate.index] = struct{}{}
		}
		report.UpdatedDomains++
		if actualWins {
			report.ImportedRoutes += max(len(chosenRoutes)-len(desiredRoutes), 0)
		}
		report.ConsolidatedDomains += len(references) - 1
	}
	if len(removeIndexes) > 0 {
		compacted := make([]any, 0, len(rawDomains)-len(removeIndexes))
		for index, raw := range rawDomains {
			if _, remove := removeIndexes[index]; !remove {
				compacted = append(compacted, raw)
			}
		}
		rawDomains = compacted
		doc["domains"] = rawDomains
	}
	if clusterAuthoritative {
		certificateBySecret := make(map[string]string)
		for key, secret := range configuredTLSSecrets(doc) {
			if key != "" && secret != "" {
				certificateBySecret[secret] = key
			}
		}
		newHosts := make([]string, 0)
		for host := range actualByHost {
			if _, configured := configuredByHost[host]; !configured {
				newHosts = append(newHosts, host)
			}
		}
		sort.Strings(newHosts)
		for _, host := range newHosts {
			routes, namespace, valid := uniqueIngressDomainRoutes(actualByHost[host])
			if !valid || len(routes) == 0 || len(routes) > 64 {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s 在 EKS 中包含冲突路由或无有效后端，未自动导入", host))
				continue
			}
			if !configuredIngressNamespace(doc, namespace) {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s 的后端 Namespace %s 不属于当前项目环境，未自动导入", host, namespace))
				continue
			}
			gateway, valid := singleIngressValue(actualClasses[host])
			if !valid {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s 在 EKS 中同时使用了不同网关，未自动导入", host))
				continue
			}
			if strings.Contains(gateway, "nginx") {
				gateway = "nginx"
			}
			if gateway != "higress" && gateway != "nginx" {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s 的 IngressClass %s 暂不支持自动导入", host, gateway))
				continue
			}
			backendProtocol, valid := singleIngressValue(actualBackendProtocols[host])
			if !valid {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s 在 EKS 中包含不同后端协议，未自动导入", host))
				continue
			}
			if backendProtocol == "" {
				backendProtocol = "http"
			}
			tlsSecret, valid := singleIngressValue(actualTLSSecrets[host])
			if !valid {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s 在 EKS 中引用了多个 TLS Secret，未自动导入", host))
				continue
			}
			tlsEnabled := tlsSecret != ""
			certificateRef := ""
			if tlsEnabled {
				certificateRef = certificateBySecret[tlsSecret]
				if certificateRef == "" {
					report.Skipped = append(report.Skipped, fmt.Sprintf("%s 的 TLS Secret %s 尚未登记到当前环境，未自动导入", host, tlsSecret))
					continue
				}
			}
			protocol := "http"
			if backendProtocol == "grpc" || backendProtocol == "grpcs" {
				if tlsEnabled {
					protocol = "grpcs"
				} else {
					protocol = "grpc"
				}
			} else if tlsEnabled {
				protocol = "https"
			}
			domain := map[string]any{
				"enabled": true, "protocol": protocol, "access_type": "domain",
				"domain": host, "gateway": gateway, "namespace": namespace,
				"routes": ingressDomainRouteDocuments(routes), "path": routes[0].path,
				"path_type": routes[0].pathType, "service": routes[0].service,
				"service_port": routes[0].servicePort, "tls_enabled": tlsEnabled,
				"certificate_ref": certificateRef, "tls_secret_name": tlsSecret,
				"backend_protocol": backendProtocol, "annotations": map[string]any{},
			}
			if name, unique := singleIngressValue(actualIngressNames[host]); unique && name != "" {
				domain["name"] = name
			}
			rawDomains = append(rawDomains, domain)
			report.UpdatedDomains++
			report.ImportedDomains++
			report.ImportedRoutes += len(routes)
		}
		doc["domains"] = rawDomains
	}
	environment.NormalizeDomainRoutes(doc)
	environment.NormalizeDomainBackendProtocols(doc)
	return report
}

func singleIngressValue(values map[string]struct{}) (string, bool) {
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	for value := range values {
		return value, true
	}
	return "", true
}

func ingressDomainRoutesEqual(left, right []ingressDomainRoute) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func configuredDomainRouteSet(references []ingressDomainReference) ([]ingressDomainRoute, string, bool) {
	routes := make([]ingressDomainRoute, 0)
	namespace := ""
	for _, reference := range references {
		currentNamespace := ingressString(reference.domain["namespace"])
		if namespace == "" {
			namespace = currentNamespace
		} else if currentNamespace != namespace {
			return nil, "", false
		}
		rawRoutes, ok := reference.domain["routes"].([]any)
		if !ok || len(rawRoutes) == 0 {
			rawRoutes = []any{reference.domain}
		}
		for _, raw := range rawRoutes {
			route := ingressMap(raw)
			port, err := strconv.Atoi(ingressPortString(route["service_port"]))
			if err != nil || port < 1 || port > 65535 || ingressString(route["service"]) == "" {
				return nil, "", false
			}
			routes = append(routes, ingressDomainRoute{
				path:     defaultIngressPath(ingressString(route["path"])),
				pathType: defaultIngressPathType(ingressString(route["path_type"])),
				service:  ingressString(route["service"]), serviceNamespace: currentNamespace, servicePort: port,
			})
		}
	}
	unique, uniqueNamespace, ok := uniqueIngressDomainRoutes(routes)
	if namespace == "" {
		namespace = uniqueNamespace
	}
	return unique, namespace, ok
}

func uniqueIngressDomainRoutes(routes []ingressDomainRoute) ([]ingressDomainRoute, string, bool) {
	byPath := make(map[string]ingressDomainRoute, len(routes))
	namespace := ""
	for _, route := range routes {
		if namespace == "" {
			namespace = route.serviceNamespace
		} else if route.serviceNamespace != namespace {
			return nil, "", false
		}
		if existing, found := byPath[route.path]; found {
			if existing != route {
				return nil, "", false
			}
			continue
		}
		byPath[route.path] = route
	}
	result := make([]ingressDomainRoute, 0, len(byPath))
	for _, route := range byPath {
		result = append(result, route)
	}
	sort.Slice(result, func(i, j int) bool {
		if len(result[i].path) == len(result[j].path) {
			return result[i].path < result[j].path
		}
		return len(result[i].path) > len(result[j].path)
	})
	return result, namespace, true
}

func compatibleDomainReferences(references []ingressDomainReference) bool {
	if len(references) < 2 {
		return true
	}
	signature := func(domain map[string]any) string {
		annotations, _ := json.Marshal(ingressStringMap(domain["annotations"]))
		return strings.Join([]string{
			strings.ToLower(ingressString(domain["protocol"])),
			strings.ToLower(ingressString(domain["gateway"])),
			strings.ToLower(ingressString(domain["backend_protocol"])),
			ingressString(domain["namespace"]),
			ingressString(domain["certificate_ref"]),
			ingressString(domain["tls_secret_name"]),
			strconv.FormatBool(ingressBoolDefault(domain["tls_enabled"], false)),
			string(annotations),
		}, "\x00")
	}
	expected := signature(references[0].domain)
	for _, reference := range references[1:] {
		if signature(reference.domain) != expected {
			return false
		}
	}
	return true
}

func ingressDomainRouteDocuments(routes []ingressDomainRoute) []any {
	result := make([]any, 0, len(routes))
	for _, route := range routes {
		result = append(result, map[string]any{
			"path": route.path, "path_type": route.pathType,
			"service": route.service, "service_port": route.servicePort,
		})
	}
	return result
}

func configuredIngressNamespace(doc environment.Document, namespace string) bool {
	for _, allowed := range ingressAllowedNamespaces(doc) {
		if allowed == namespace {
			return true
		}
	}
	return false
}

func defaultIngressPath(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "/"
}

func defaultIngressPathType(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "Prefix"
}

func setContains(values map[string]struct{}, value string) bool {
	_, exists := values[value]
	return exists
}

func ingressPortString(value any) string {
	if text := ingressString(value); text != "" {
		return text
	}
	if number := ingressInt(value); number > 0 {
		return strconv.Itoa(number)
	}
	return ""
}

func ingressBoolDefault(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	result, ok := value.(bool)
	if !ok {
		return fallback
	}
	return result
}

func (s *Service) currentIngressResourceVersion(ctx context.Context, kubeconfig, namespace, ingressName string) (string, bool, error) {
	payload, err := s.capture(ctx, "", s.config.Tools.Kubectl, []string{
		"get", "ingresses.networking.k8s.io", ingressName, "--namespace", namespace,
		"--output", "json", "--ignore-not-found=true",
	}, kubeconfig)
	if err != nil {
		return "", false, fmt.Errorf("检查 Ingress %s/%s 当前版本失败: %w", namespace, ingressName, err)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return "", false, nil
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		return "", false, errors.New("Kubernetes 返回的 Ingress 版本信息无效")
	}
	metadata := ingressMap(document["metadata"])
	version := ingressString(metadata["resourceVersion"])
	return version, version != "", nil
}

func (s *Service) captureIngressInput(ctx context.Context, executable string, args []string, kubeconfig string, input []byte) ([]byte, error) {
	if err := s.acquireCommandSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseCommandSlot()
	commandCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, executable, args...) // #nosec G204 -- executable is administrator configured and arguments never pass through a shell.
	env := removeEnvironmentKeys(os.Environ(), "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_DEFAULT_PROFILE")
	if projectEnvironment, ok := ctx.Value(awsEnvironmentContextKey{}).([]string); ok {
		env = append(env, projectEnvironment...)
	}
	env = append(env, "KUBECONFIG="+kubeconfig)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", executable, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func ingressAllowedNamespaces(doc environment.Document) []string {
	raw, ok := doc["namespaces"].(map[string]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for namespace := range raw {
		namespace = strings.TrimSpace(namespace)
		if namespace != "" {
			result = append(result, namespace)
		}
	}
	sort.Strings(result)
	return result
}

func validateIngressIdentity(doc environment.Document, namespace, ingressName string) error {
	namespace, ingressName = strings.TrimSpace(namespace), strings.TrimSpace(ingressName)
	if len(namespace) > 63 || len(ingressName) > 253 || !kubernetesObjectNamePattern.MatchString(namespace) || !kubernetesObjectNamePattern.MatchString(ingressName) {
		return errors.New("Ingress Namespace 或名称不符合 Kubernetes 命名规则")
	}
	for _, allowed := range ingressAllowedNamespaces(doc) {
		if namespace == allowed {
			return nil
		}
	}
	return fmt.Errorf("Namespace %s 不属于当前项目环境，平台已拒绝操作", namespace)
}

func normalizeIngressYAML(doc environment.Document, source []byte) ([]byte, KubernetesIngress, []string, error) {
	if len(bytes.TrimSpace(source)) == 0 {
		return nil, KubernetesIngress{}, nil, errors.New("Ingress YAML 不能为空")
	}
	if len(source) > maxIngressYAMLBytes {
		return nil, KubernetesIngress{}, nil, fmt.Errorf("Ingress YAML 不能超过 %d KiB", maxIngressYAMLBytes/1024)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, KubernetesIngress{}, nil, fmt.Errorf("Ingress YAML 格式错误: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, KubernetesIngress{}, nil, errors.New("一次只能编辑一个 Ingress，不能提交多段 YAML")
		}
		return nil, KubernetesIngress{}, nil, fmt.Errorf("Ingress YAML 格式错误: %w", err)
	}
	if yamlContainsAlias(&root) {
		return nil, KubernetesIngress{}, nil, errors.New("出于安全限制，Ingress YAML 不允许使用锚点或别名")
	}
	var document map[string]any
	if err := root.Decode(&document); err != nil {
		return nil, KubernetesIngress{}, nil, fmt.Errorf("Ingress YAML 格式错误: %w", err)
	}
	if ingressString(document["apiVersion"]) != "networking.k8s.io/v1" || !strings.EqualFold(ingressString(document["kind"]), "Ingress") {
		return nil, KubernetesIngress{}, nil, errors.New("只允许 networking.k8s.io/v1 Ingress，禁止通过编辑器操作其他 Kubernetes 资源")
	}
	metadata := ingressMap(document["metadata"])
	namespace, ingressName := ingressString(metadata["namespace"]), ingressString(metadata["name"])
	if err := validateIngressIdentity(doc, namespace, ingressName); err != nil {
		return nil, KubernetesIngress{}, nil, err
	}
	if len(ingressMap(metadata["ownerReferences"])) > 0 || len(ingressSlice(metadata["ownerReferences"])) > 0 || len(ingressSlice(metadata["finalizers"])) > 0 {
		return nil, KubernetesIngress{}, nil, errors.New("在线编辑器不允许设置 ownerReferences 或 finalizers")
	}
	annotations := ingressStringMap(metadata["annotations"])
	for key := range annotations {
		lower := strings.ToLower(strings.TrimSpace(key))
		if strings.Contains(lower, "snippet") || strings.HasSuffix(lower, "/auth-url") ||
			strings.HasSuffix(lower, "/mirror-target") {
			return nil, KubernetesIngress{}, nil, fmt.Errorf("出于安全限制，不允许使用高风险 Ingress 注解 %s", key)
		}
	}
	spec := ingressMap(document["spec"])
	className := ingressString(spec["ingressClassName"])
	if className == "" {
		className = annotations["kubernetes.io/ingress.class"]
	}
	if className == "" {
		return nil, KubernetesIngress{}, nil, errors.New("Ingress 必须配置 spec.ingressClassName")
	}
	rules := ingressSlice(spec["rules"])
	defaultBackend := ingressMap(spec["defaultBackend"])
	if len(rules) == 0 && len(defaultBackend) == 0 {
		return nil, KubernetesIngress{}, nil, errors.New("Ingress 至少需要一条 rules 规则或 defaultBackend")
	}
	if len(defaultBackend) > 0 {
		service := ingressMap(defaultBackend["service"])
		port := ingressMap(service["port"])
		if ingressString(service["name"]) == "" || (ingressInt(port["number"]) <= 0 && ingressString(port["name"]) == "") {
			return nil, KubernetesIngress{}, nil, errors.New("Ingress defaultBackend 必须指定后端 Service 和端口")
		}
	}
	for index, rawRule := range rules {
		rule := ingressMap(rawRule)
		httpConfig := ingressMap(rule["http"])
		paths := ingressSlice(httpConfig["paths"])
		if len(paths) == 0 {
			return nil, KubernetesIngress{}, nil, fmt.Errorf("Ingress rules[%d] 至少需要一个 HTTP path", index)
		}
		for pathIndex, rawPath := range paths {
			backend := ingressMap(ingressMap(rawPath)["backend"])
			service := ingressMap(backend["service"])
			port := ingressMap(service["port"])
			if ingressString(service["name"]) == "" || (ingressInt(port["number"]) <= 0 && ingressString(port["name"]) == "") {
				return nil, KubernetesIngress{}, nil, fmt.Errorf("Ingress rules[%d].paths[%d] 必须指定后端 Service 和端口", index, pathIndex)
			}
		}
	}
	delete(document, "status")
	for _, key := range []string{"managedFields", "uid", "generation", "creationTimestamp", "resourceVersion", "selfLink"} {
		delete(metadata, key)
	}
	delete(metadata, "ownerReferences")
	delete(metadata, "finalizers")
	labels := ingressStringMap(metadata["labels"])
	labels["ops-deploy.io/project"] = stringAt(doc, "project")
	labels["ops-deploy.io/environment"] = stringAt(doc, "environment")
	labels["ops-deploy.io/managed-by"] = "ingress-editor"
	metadata["labels"] = labels
	metadata["namespace"], metadata["name"] = namespace, ingressName
	document["metadata"], document["spec"] = metadata, spec
	normalized, err := yaml.Marshal(document)
	if err != nil {
		return nil, KubernetesIngress{}, nil, fmt.Errorf("规范化 Ingress YAML 失败: %w", err)
	}
	jsonPayload, _ := json.Marshal(document)
	summary, err := decodeKubernetesIngress(jsonPayload)
	if err != nil {
		return nil, KubernetesIngress{}, nil, errors.New("Ingress YAML 结构无效")
	}
	warnings := make([]string, 0, 2)
	if len(summary.TLSSecrets) == 0 {
		warnings = append(warnings, "该 Ingress 未配置 TLS；公网业务建议使用 HTTPS/WSS/GRPCS。")
	}
	if annotations["kubernetes.io/ingress.class"] != "" {
		warnings = append(warnings, "检测到旧式 kubernetes.io/ingress.class 注解，建议迁移到 spec.ingressClassName。")
	}
	return normalized, summary, warnings, nil
}

func editableIngressDocument(payload []byte) (KubernetesIngressDocument, error) {
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		return KubernetesIngressDocument{}, errors.New("Kubernetes 返回的 Ingress 数据格式无效")
	}
	summary, err := decodeKubernetesIngress(payload)
	if err != nil {
		return KubernetesIngressDocument{}, err
	}
	delete(document, "status")
	metadata := ingressMap(document["metadata"])
	for _, key := range []string{"managedFields", "uid", "generation", "creationTimestamp", "selfLink"} {
		delete(metadata, key)
	}
	document["metadata"] = metadata
	editable, err := yaml.Marshal(document)
	if err != nil {
		return KubernetesIngressDocument{}, err
	}
	return KubernetesIngressDocument{Ingress: summary, YAML: string(editable)}, nil
}

func (s *Service) validateIngressBackends(ctx context.Context, kubeconfig string, ingress KubernetesIngress) error {
	if len(ingress.Paths) == 0 {
		return nil
	}
	servicePayload, err := s.capture(ctx, "", s.config.Tools.Kubectl, []string{
		"get", "services", "--namespace", ingress.Namespace, "--output", "json",
	}, kubeconfig)
	if err != nil {
		return fmt.Errorf("读取 Namespace %s 的 Service 失败，无法验证 Ingress 后端: %w", ingress.Namespace, err)
	}
	services, err := decodeKubernetesServices(servicePayload)
	if err != nil {
		return errors.New("Kubernetes 返回的 Service 数据格式无效")
	}
	serviceIndex := make(map[string]KubernetesService, len(services))
	for _, service := range services {
		serviceIndex[service.Name] = service
	}
	for _, path := range ingress.Paths {
		service, exists := serviceIndex[path.ServiceName]
		if !exists {
			return fmt.Errorf("Ingress 后端 Service %s/%s 不存在", ingress.Namespace, path.ServiceName)
		}
		portFound := false
		for _, port := range service.Ports {
			if path.ServicePort == port.Name || path.ServicePort == strconv.Itoa(port.Port) {
				portFound = true
				break
			}
		}
		if !portFound {
			return fmt.Errorf("Ingress 后端 Service %s/%s 不包含端口 %s", ingress.Namespace, path.ServiceName, path.ServicePort)
		}
		if strings.EqualFold(service.Type, "ExternalName") {
			continue
		}
		endpointPayload, endpointErr := s.capture(ctx, "", s.config.Tools.Kubectl, []string{
			"get", "endpointslices.discovery.k8s.io", "--namespace", ingress.Namespace,
			"--selector", "kubernetes.io/service-name=" + path.ServiceName, "--output", "json",
		}, kubeconfig)
		if endpointErr != nil {
			return fmt.Errorf("读取 Service %s/%s 的 EndpointSlice 失败: %w", ingress.Namespace, path.ServiceName, endpointErr)
		}
		health, decodeErr := decodeKubernetesEndpointHealth(endpointPayload)
		if decodeErr != nil {
			return fmt.Errorf("解析 Service %s/%s 的 EndpointSlice 失败", ingress.Namespace, path.ServiceName)
		}
		if health[ingress.Namespace+"/"+path.ServiceName].Ready == 0 {
			return fmt.Errorf("Ingress 后端 Service %s/%s 当前没有 Ready Endpoint，保存后会出现 no healthy upstream；请先恢复对应 Pod", ingress.Namespace, path.ServiceName)
		}
	}
	return nil
}

func decodeKubernetesIngressList(payload []byte) ([]KubernetesIngress, error) {
	var response struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	result := make([]KubernetesIngress, 0, len(response.Items))
	for _, item := range response.Items {
		ingress, err := decodeKubernetesIngress(item)
		if err != nil {
			return nil, err
		}
		result = append(result, ingress)
	}
	return result, nil
}

// DecodeKubernetesIngressList exposes the same safe decoder used by the
// platform API for maintenance commands that only read cluster inventory.
func DecodeKubernetesIngressList(payload []byte) ([]KubernetesIngress, error) {
	return decodeKubernetesIngressList(payload)
}

func decodeKubernetesIngress(payload []byte) (KubernetesIngress, error) {
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		return KubernetesIngress{}, err
	}
	metadata, spec, status := ingressMap(document["metadata"]), ingressMap(document["spec"]), ingressMap(document["status"])
	annotations := ingressStringMap(metadata["annotations"])
	result := KubernetesIngress{
		Name: ingressString(metadata["name"]), Namespace: ingressString(metadata["namespace"]),
		ClassName: ingressString(spec["ingressClassName"]), ResourceVersion: ingressString(metadata["resourceVersion"]),
		CreationTimestamp: ingressString(metadata["creationTimestamp"]), Hosts: make([]string, 0),
		Paths: make([]KubernetesIngressPath, 0), TLSSecrets: make([]string, 0), Addresses: make([]string, 0),
		ManagedBy: ingressStringMap(metadata["labels"])["ops-deploy.io/managed-by"],
	}
	result.BackendProtocol = strings.ToLower(annotations["higress.io/backend-protocol"])
	if result.BackendProtocol == "" {
		result.BackendProtocol = strings.ToLower(annotations["nginx.ingress.kubernetes.io/backend-protocol"])
	}
	if result.ClassName == "" {
		result.ClassName = annotations["kubernetes.io/ingress.class"]
	}
	if defaultBackend := ingressMap(spec["defaultBackend"]); len(defaultBackend) > 0 {
		service := ingressMap(defaultBackend["service"])
		port := ingressMap(service["port"])
		servicePort := ingressString(port["name"])
		if number := ingressInt(port["number"]); number > 0 {
			servicePort = strconv.Itoa(number)
		}
		if ingressString(service["name"]) != "" {
			result.Paths = append(result.Paths, KubernetesIngressPath{
				Path: "/", PathType: "DefaultBackend", ServiceName: ingressString(service["name"]),
				ServiceNamespace: result.Namespace, ServicePort: servicePort,
			})
		}
	}
	destinationService, destinationNamespace, destinationPort := parseHigressDestination(
		annotations["higress.io/destination"], result.Namespace,
	)
	for _, rawRule := range ingressSlice(spec["rules"]) {
		rule := ingressMap(rawRule)
		host := ingressString(rule["host"])
		if host != "" {
			result.Hosts = appendUniqueString(result.Hosts, host)
		}
		for _, rawPath := range ingressSlice(ingressMap(rule["http"])["paths"]) {
			path := ingressMap(rawPath)
			service := ingressMap(ingressMap(path["backend"])["service"])
			port := ingressMap(service["port"])
			servicePort := ingressString(port["name"])
			if number := ingressInt(port["number"]); number > 0 {
				servicePort = strconv.Itoa(number)
			}
			serviceName, serviceNamespace := ingressString(service["name"]), result.Namespace
			if serviceName == "" && destinationService != "" {
				serviceName, serviceNamespace, servicePort = destinationService, destinationNamespace, destinationPort
			}
			result.Paths = append(result.Paths, KubernetesIngressPath{
				Host: host, Path: ingressString(path["path"]), PathType: ingressString(path["pathType"]),
				ServiceName: serviceName, ServiceNamespace: serviceNamespace, ServicePort: servicePort,
			})
		}
	}
	for _, rawTLS := range ingressSlice(spec["tls"]) {
		if secretName := ingressString(ingressMap(rawTLS)["secretName"]); secretName != "" {
			result.TLSSecrets = appendUniqueString(result.TLSSecrets, secretName)
		}
	}
	loadBalancer := ingressMap(status["loadBalancer"])
	for _, rawAddress := range ingressSlice(loadBalancer["ingress"]) {
		address := ingressMap(rawAddress)
		value := ingressString(address["hostname"])
		if value == "" {
			value = ingressString(address["ip"])
		}
		if value != "" {
			result.Addresses = appendUniqueString(result.Addresses, value)
		}
	}
	if result.Name == "" || result.Namespace == "" {
		return KubernetesIngress{}, errors.New("Ingress 缺少 metadata.name 或 metadata.namespace")
	}
	return result, nil
}

func parseHigressDestination(value, fallbackNamespace string) (string, string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", ""
	}
	if separator := strings.Index(value, ","); separator >= 0 {
		value = value[:separator]
	}
	if separator := strings.Index(value, "://"); separator >= 0 {
		value = value[separator+3:]
	}
	value = strings.TrimSpace(strings.SplitN(value, "/", 2)[0])
	lastColon := strings.LastIndex(value, ":")
	if lastColon <= 0 || lastColon == len(value)-1 {
		return "", "", ""
	}
	host, port := strings.TrimSuffix(value[:lastColon], "."), value[lastColon+1:]
	if parsed, err := strconv.Atoi(port); err != nil || parsed < 1 || parsed > 65535 {
		return "", "", ""
	}
	parts := strings.Split(host, ".")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return "", "", ""
	}
	namespace := strings.TrimSpace(fallbackNamespace)
	if len(parts) >= 2 && parts[1] != "svc" {
		namespace = strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(parts[0]), namespace, port
}

func ingressMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func ingressSlice(value any) []any {
	result, _ := value.([]any)
	return result
}

func ingressString(value any) string {
	result, _ := value.(string)
	return strings.TrimSpace(result)
}

func ingressStringMap(value any) map[string]string {
	result := make(map[string]string)
	switch typed := value.(type) {
	case map[string]string:
		for key, item := range typed {
			result[key] = item
		}
	case map[string]any:
		for key, item := range typed {
			if text, ok := item.(string); ok {
				result[key] = text
			}
		}
	}
	return result
}

func ingressInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case uint64:
		return int(typed)
	}
	return 0
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func yamlContainsAlias(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return true
	}
	for _, child := range node.Content {
		if yamlContainsAlias(child) {
			return true
		}
	}
	return false
}
