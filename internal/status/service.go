package status

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
)

type Report struct {
	Project         string              `json:"project,omitempty"`
	Environment     string              `json:"environment"`
	EnvironmentName string              `json:"environment_name,omitempty"`
	TargetName      string              `json:"target_name,omitempty"`
	ObservedAt      time.Time           `json:"observed_at"`
	Cluster         Cluster             `json:"cluster"`
	Nodes           []Node              `json:"nodes"`
	Pods            PodSummary          `json:"pods"`
	Components      []Component         `json:"components"`
	Releases        []HelmRelease       `json:"releases"`
	Services        []KubernetesService `json:"services"`
	Outputs         map[string]any      `json:"outputs"`
	Warnings        []string            `json:"warnings"`
}

type Cluster struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Version    string `json:"version,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	TargetType string `json:"target_type,omitempty"`
	Reachable  bool   `json:"reachable"`
}

type Node struct {
	Name         string `json:"name"`
	Ready        bool   `json:"ready"`
	InstanceType string `json:"instance_type,omitempty"`
	Zone         string `json:"zone,omitempty"`
	Version      string `json:"version,omitempty"`
}

type PodSummary struct {
	Total     int            `json:"total"`
	Ready     int            `json:"ready"`
	Pending   int            `json:"pending"`
	Failed    int            `json:"failed"`
	Unhealthy []UnhealthyPod `json:"unhealthy"`
}

type UnhealthyPod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	Reason    string `json:"reason,omitempty"`
}

type Component struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Category    string `json:"category"`
	Desired     bool   `json:"desired"`
	Actual      bool   `json:"actual"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
}

type HelmRelease struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Status     string `json:"status"`
	Chart      string `json:"chart"`
	AppVersion string `json:"app_version"`
}

type KubernetesService struct {
	Name                string        `json:"name"`
	Namespace           string        `json:"namespace"`
	Type                string        `json:"type"`
	Ports               []ServicePort `json:"ports"`
	LoadBalancerHosts   []string      `json:"load_balancer_hosts"`
	EndpointHealthKnown bool          `json:"endpoint_health_known"`
	ReadyEndpoints      int           `json:"ready_endpoints"`
	TotalEndpoints      int           `json:"total_endpoints"`
}

type ServicePort struct {
	Name        string `json:"name,omitempty"`
	Port        int    `json:"port"`
	AppProtocol string `json:"app_protocol,omitempty"`
}

type Service struct {
	config         *appconfig.Config
	environments   *environment.Repository
	cache          Cache
	awsProvider    AWSCredentialProvider
	outputProvider TerraformOutputProvider
	operations     *operationalCache
	commandSlots   chan struct{}
}

type AWSCredentialProvider interface {
	Environment(context.Context, string) ([]string, error)
}

type TerraformOutputProvider interface {
	StateOutputs(context.Context, string, string, string) (map[string]any, error)
}

type awsEnvironmentContextKey struct{}

type Cache interface {
	GetStatus(context.Context, string) ([]byte, bool, error)
	SetStatus(context.Context, string, []byte) error
	DeleteStatus(context.Context, string) error
}

type batchCache interface {
	GetStatuses(context.Context, []string) (map[string][]byte, error)
}

func NewService(config *appconfig.Config, environments *environment.Repository) *Service {
	return NewServiceWithCache(config, environments, nil)
}

func NewServiceWithCache(config *appconfig.Config, environments *environment.Repository, cache Cache) *Service {
	return &Service{
		config: config, environments: environments, cache: cache,
		operations: newOperationalCache(),
		// Live status pages are read-only but still launch AWS CLI and kubectl
		// processes. Bounding them prevents a burst of browser refreshes from
		// exhausting CPU, memory, file descriptors, or AWS API quotas.
		commandSlots: make(chan struct{}, 6),
	}
}

func (s *Service) SetAWSCredentialProvider(provider AWSCredentialProvider) {
	s.awsProvider = provider
}

func (s *Service) SetTerraformOutputProvider(provider TerraformOutputProvider) {
	s.outputProvider = provider
}

// Cached returns the last short-lived observation without contacting AWS or
// the Kubernetes API. Project listings use it as an optional health signal so
// opening the project page never fans out into slow cloud requests.
func (s *Service) Cached(ctx context.Context, name string) (*Report, bool, error) {
	if s.cache == nil {
		return nil, false, nil
	}
	payload, found, err := s.cache.GetStatus(ctx, name)
	if err != nil || !found {
		return nil, found, err
	}
	var report Report
	if err := json.Unmarshal(payload, &report); err != nil {
		return nil, false, err
	}
	return &report, true, nil
}

// CachedMany reads project-card status snapshots in one cache round trip when
// the backing store supports it. Corrupt or missing entries are intentionally
// omitted because the project list treats health as an optional hint.
func (s *Service) CachedMany(ctx context.Context, names []string) map[string]*Report {
	result := make(map[string]*Report)
	if s.cache == nil || len(names) == 0 {
		return result
	}
	if cache, ok := s.cache.(batchCache); ok {
		payloads, err := cache.GetStatuses(ctx, names)
		if err != nil {
			return result
		}
		for name, payload := range payloads {
			var report Report
			if json.Unmarshal(payload, &report) == nil {
				result[name] = &report
			}
		}
		return result
	}
	for _, name := range names {
		report, found, err := s.Cached(ctx, name)
		if err == nil && found {
			result[name] = report
		}
	}
	return result
}

func (s *Service) Collect(ctx context.Context, name string) (*Report, error) {
	return s.collect(ctx, name, false)
}

func (s *Service) CollectFresh(ctx context.Context, name string) (*Report, error) {
	payload, err := loadOperationalValue(ctx, s.operations, operationalCacheKey(name, "fresh-status"), 2*time.Second, func() ([]byte, error) {
		report, collectErr := s.collect(ctx, name, true)
		if collectErr != nil {
			return nil, collectErr
		}
		encoded, marshalErr := json.Marshal(report)
		if marshalErr != nil {
			return nil, marshalErr
		}
		return encoded, nil
	})
	if err != nil {
		return nil, err
	}
	var report Report
	if err := json.Unmarshal(payload, &report); err != nil {
		return nil, err
	}
	return &report, nil
}

// ListServices performs a focused, live EKS query for the gateway backend
// selector. It intentionally avoids the heavier node, pod, Helm and add-on
// collection used by the environment overview.
func (s *Service) ListServices(ctx context.Context, name string) ([]KubernetesService, error) {
	services, err := loadOperationalValue(ctx, s.operations, operationalCacheKey(name, "services"), 15*time.Second, func() ([]KubernetesService, error) {
		commandContext, _, kubeconfig, contextErr := s.kubernetesContext(ctx, name)
		if contextErr != nil {
			return nil, contextErr
		}
		defer os.Remove(kubeconfig)
		allowed, captureErr := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{"auth", "can-i", "list", "services", "--all-namespaces"}, kubeconfig)
		if captureErr != nil || !strings.EqualFold(strings.TrimSpace(string(allowed)), "yes") {
			return nil, errors.New("当前项目 AWS 身份没有跨 Namespace 读取 Kubernetes Service 的权限，请检查 EKS Access Entry 或 RBAC")
		}
		payload, captureErr := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{"get", "services", "-A", "-o", "json"}, kubeconfig)
		if captureErr != nil {
			return nil, errors.New("读取 EKS Service 失败，请检查 Kubernetes API 连通性和 RBAC 权限")
		}
		items, decodeErr := decodeKubernetesServices(payload)
		if decodeErr != nil {
			return nil, errors.New("EKS 返回的 Service 数据格式无效")
		}
		endpointPayload, endpointErr := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{"get", "endpointslices.discovery.k8s.io", "-A", "-o", "json"}, kubeconfig)
		if endpointErr == nil {
			if health, healthErr := decodeKubernetesEndpointHealth(endpointPayload); healthErr == nil {
				applyKubernetesEndpointHealth(items, health)
			}
		}
		return items, nil
	})
	if err != nil {
		return nil, err
	}
	return cloneKubernetesServices(services), nil
}

func (s *Service) collect(ctx context.Context, name string, fresh bool) (*Report, error) {
	doc, err := s.environments.Load(name)
	if err != nil {
		return nil, err
	}
	doc = environment.ApplyDefaults(doc, stringAt(doc, "project"), stringAt(doc, "environment"))
	project := stringAt(doc, "project")
	if s.awsProvider == nil {
		return nil, errors.New("当前项目未绑定 AWS 凭据，无法采集 AWS 或 EKS 状态")
	}
	awsEnvironment, credentialErr := s.awsProvider.Environment(ctx, project)
	if credentialErr != nil {
		return nil, fmt.Errorf("当前项目未绑定可用的 AWS 凭据，状态采集已停止: %w", credentialErr)
	}
	if len(awsEnvironment) == 0 {
		return nil, errors.New("当前项目未绑定可用的 AWS 凭据，状态采集已停止")
	}
	ctx = context.WithValue(ctx, awsEnvironmentContextKey{}, awsEnvironment)
	if !fresh && s.cache != nil {
		if payload, found, cacheErr := s.cache.GetStatus(ctx, name); cacheErr == nil && found {
			var cached Report
			if json.Unmarshal(payload, &cached) == nil {
				return &cached, nil
			}
		}
	}
	region := stringAt(doc, "region")
	clusterName := environment.ClusterName(doc)
	report := &Report{
		Environment: name,
		ObservedAt:  time.Now().UTC(),
		Nodes:       make([]Node, 0),
		Components:  make([]Component, 0),
		Releases:    make([]HelmRelease, 0),
		Services:    make([]KubernetesService, 0),
		Warnings:    make([]string, 0),
		Pods: PodSummary{
			Unhealthy: make([]UnhealthyPod, 0),
		},
		Cluster: Cluster{
			Name:       clusterName,
			Status:     "NOT_FOUND",
			TargetType: environment.TargetType(doc),
		},
		Outputs: make(map[string]any),
	}

	outputsReady := make(chan *Report, 1)
	go func() {
		local := &Report{Outputs: make(map[string]any), Warnings: make([]string, 0)}
		s.collectTerraformOutputs(ctx, project, name, local)
		outputsReady <- local
	}()
	clusterOutput, err := s.capture(ctx, "", s.config.Tools.AWS, []string{
		"eks", "describe-cluster", "--region", region, "--name", clusterName,
		"--query", "cluster.{status:status,version:version,endpoint:endpoint}", "--output", "json",
	}, "")
	outputReport := <-outputsReady
	report.Outputs = outputReport.Outputs
	report.Warnings = append(report.Warnings, outputReport.Warnings...)
	if err != nil {
		report.Warnings = append(report.Warnings, "EKS cluster is not reachable through the configured AWS identity")
		report.Components = s.componentStatuses(doc, nil, nil, nil, report.Outputs)
		s.cacheReport(ctx, name, report)
		return report, nil
	}
	var cluster struct {
		Status   string `json:"status"`
		Version  string `json:"version"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal(clusterOutput, &cluster); err != nil {
		return nil, err
	}
	report.Cluster.Status = cluster.Status
	report.Cluster.Version = cluster.Version
	report.Cluster.Endpoint = cluster.Endpoint
	report.Cluster.Reachable = cluster.Status == "ACTIVE"

	installedAddons := make(map[string]string)
	if addonOutput, err := s.capture(ctx, "", s.config.Tools.AWS, []string{
		"eks", "list-addons", "--region", region, "--cluster-name", clusterName, "--output", "json",
	}, ""); err == nil {
		var response struct {
			Addons []string `json:"addons"`
		}
		if json.Unmarshal(addonOutput, &response) == nil {
			installedAddons = s.collectAddonStatuses(ctx, region, clusterName, response.Addons)
		}
	}

	if report.Cluster.Reachable {
		kubeconfig, err := s.updateKubeconfig(ctx, name, region, clusterName)
		if err != nil {
			report.Cluster.Reachable = false
			report.Warnings = append(report.Warnings, "EKS 处于 ACTIVE，但当前 AWS 身份无法生成 kubeconfig")
		} else {
			// Status collection must never share the deployment kubeconfig. UI
			// refreshes can run concurrently with Terraform/Helm and AWS CLI
			// rewrites kubeconfig files in place.
			defer os.Remove(kubeconfig)
			allowed, authErr := s.capture(ctx, "", s.config.Tools.Kubectl, []string{"auth", "can-i", "*", "*", "--all-namespaces"}, kubeconfig)
			if authErr != nil || !strings.EqualFold(strings.TrimSpace(string(allowed)), "yes") {
				report.Cluster.Reachable = false
				report.Warnings = append(report.Warnings, "EKS 处于 ACTIVE，但当前 AWS 身份没有 Kubernetes 集群管理权限；请配置 EKS Access Entry 或 aws-auth")
			} else {
				s.collectKubernetesWorkloads(ctx, kubeconfig, report)
			}
		}
	}

	releases := make(map[string]HelmRelease, len(report.Releases))
	for _, release := range report.Releases {
		releases[release.Name] = release
	}
	report.Components = s.componentStatuses(doc, installedAddons, releases, report.Services, report.Outputs)
	s.cacheReport(ctx, name, report)
	return report, nil
}

func (s *Service) collectAddonStatuses(ctx context.Context, region, clusterName string, addons []string) map[string]string {
	type addonResult struct {
		name   string
		status string
	}
	results := make(chan addonResult, len(addons))
	localSlots := make(chan struct{}, 4)
	for _, addonName := range addons {
		addon := addonName
		go func() {
			select {
			case localSlots <- struct{}{}:
				defer func() { <-localSlots }()
			case <-ctx.Done():
				results <- addonResult{name: addon, status: "UNKNOWN"}
				return
			}
			statusOutput, err := s.capture(ctx, "", s.config.Tools.AWS, []string{
				"eks", "describe-addon", "--region", region, "--cluster-name", clusterName,
				"--addon-name", addon, "--query", "addon.status", "--output", "text",
			}, "")
			status := "UNKNOWN"
			if err == nil {
				status = strings.TrimSpace(string(statusOutput))
			}
			results <- addonResult{name: addon, status: status}
		}()
	}
	statuses := make(map[string]string, len(addons))
	for range addons {
		result := <-results
		statuses[result.name] = result.status
	}
	return statuses
}

func (s *Service) collectKubernetesWorkloads(ctx context.Context, kubeconfig string, report *Report) {
	collectors := []func(*Report){
		func(local *Report) { s.collectNodes(ctx, kubeconfig, local) },
		func(local *Report) { s.collectPods(ctx, kubeconfig, local) },
		func(local *Report) { s.collectHelm(ctx, kubeconfig, local) },
		func(local *Report) { s.collectServices(ctx, kubeconfig, local) },
	}
	type collectionResult struct {
		index  int
		report *Report
	}
	results := make(chan collectionResult, len(collectors))
	for index, collector := range collectors {
		go func() {
			local := &Report{Pods: PodSummary{Unhealthy: make([]UnhealthyPod, 0)}}
			collector(local)
			results <- collectionResult{index: index, report: local}
		}()
	}
	collected := make([]*Report, len(collectors))
	for range collectors {
		result := <-results
		collected[result.index] = result.report
	}
	report.Nodes = collected[0].Nodes
	report.Pods = collected[1].Pods
	report.Releases = collected[2].Releases
	report.Services = collected[3].Services
	for _, local := range collected {
		report.Warnings = append(report.Warnings, local.Warnings...)
	}
}

func (s *Service) cacheReport(ctx context.Context, name string, report *Report) {
	if s.cache == nil {
		return
	}
	if payload, err := json.Marshal(report); err == nil {
		_ = s.cache.SetStatus(ctx, name, payload)
	}
}

func (s *Service) Invalidate(ctx context.Context, name string) {
	if s.cache != nil {
		_ = s.cache.DeleteStatus(ctx, name)
	}
	s.operations.deleteTarget(name)
}

func (s *Service) collectNodes(ctx context.Context, kubeconfig string, report *Report) {
	b, err := s.capture(ctx, "", s.config.Tools.Kubectl, []string{"get", "nodes", "-o", "json"}, kubeconfig)
	if err != nil {
		report.Warnings = append(report.Warnings, "Could not list Kubernetes nodes")
		return
	}
	var response struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				NodeInfo struct {
					KubeletVersion string `json:"kubeletVersion"`
				} `json:"nodeInfo"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(b, &response) != nil {
		return
	}
	for _, item := range response.Items {
		ready := false
		for _, condition := range item.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				ready = true
			}
		}
		report.Nodes = append(report.Nodes, Node{
			Name:         item.Metadata.Name,
			Ready:        ready,
			InstanceType: item.Metadata.Labels["node.kubernetes.io/instance-type"],
			Zone:         item.Metadata.Labels["topology.kubernetes.io/zone"],
			Version:      item.Status.NodeInfo.KubeletVersion,
		})
	}
}

func (s *Service) collectPods(ctx context.Context, kubeconfig string, report *Report) {
	b, err := s.capture(ctx, "", s.config.Tools.Kubectl, []string{"get", "pods", "-A", "-o", "json"}, kubeconfig)
	if err != nil {
		report.Warnings = append(report.Warnings, "Could not list Kubernetes Pods")
		return
	}
	var response struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				Phase             string `json:"phase"`
				Reason            string `json:"reason"`
				ContainerStatuses []struct {
					Ready bool `json:"ready"`
					State struct {
						Waiting *struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(b, &response) != nil {
		return
	}
	for _, item := range response.Items {
		report.Pods.Total++
		allReady := len(item.Status.ContainerStatuses) > 0
		reason := item.Status.Reason
		for _, container := range item.Status.ContainerStatuses {
			allReady = allReady && container.Ready
			if reason == "" && container.State.Waiting != nil {
				reason = container.State.Waiting.Reason
			}
		}
		if item.Status.Phase == "Succeeded" || (item.Status.Phase == "Running" && allReady) {
			report.Pods.Ready++
			continue
		}
		switch item.Status.Phase {
		case "Pending":
			report.Pods.Pending++
		case "Failed":
			report.Pods.Failed++
		}
		if len(report.Pods.Unhealthy) < 25 {
			report.Pods.Unhealthy = append(report.Pods.Unhealthy, UnhealthyPod{
				Namespace: item.Metadata.Namespace,
				Name:      item.Metadata.Name,
				Phase:     item.Status.Phase,
				Reason:    reason,
			})
		}
	}
}

func (s *Service) collectHelm(ctx context.Context, kubeconfig string, report *Report) {
	b, err := s.capture(ctx, "", s.config.Tools.Helm, []string{"list", "-A", "-o", "json"}, kubeconfig)
	if err != nil {
		report.Warnings = append(report.Warnings, "Could not list Helm releases")
		return
	}
	if err := json.Unmarshal(b, &report.Releases); err != nil {
		return
	}
	sort.Slice(report.Releases, func(i, j int) bool { return report.Releases[i].Name < report.Releases[j].Name })
}

func (s *Service) collectServices(ctx context.Context, kubeconfig string, report *Report) {
	b, err := s.capture(ctx, "", s.config.Tools.Kubectl, []string{"get", "services", "-A", "-o", "json"}, kubeconfig)
	if err != nil {
		report.Warnings = append(report.Warnings, "Could not list Kubernetes Services")
		return
	}
	services, err := decodeKubernetesServices(b)
	if err != nil {
		report.Warnings = append(report.Warnings, "Could not decode Kubernetes Services")
		return
	}
	if endpointPayload, endpointErr := s.capture(ctx, "", s.config.Tools.Kubectl, []string{"get", "endpointslices.discovery.k8s.io", "-A", "-o", "json"}, kubeconfig); endpointErr == nil {
		if health, decodeErr := decodeKubernetesEndpointHealth(endpointPayload); decodeErr == nil {
			applyKubernetesEndpointHealth(services, health)
		}
	}
	report.Services = append(report.Services, services...)
}

type kubernetesEndpointHealth struct {
	Ready int
	Total int
}

func decodeKubernetesEndpointHealth(b []byte) (map[string]kubernetesEndpointHealth, error) {
	var response struct {
		Items []struct {
			Metadata struct {
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Endpoints []struct {
				Addresses  []string `json:"addresses"`
				Conditions struct {
					Ready       *bool `json:"ready"`
					Terminating *bool `json:"terminating"`
				} `json:"conditions"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &response); err != nil {
		return nil, err
	}
	health := make(map[string]kubernetesEndpointHealth)
	for _, item := range response.Items {
		serviceName := strings.TrimSpace(item.Metadata.Labels["kubernetes.io/service-name"])
		namespace := strings.TrimSpace(item.Metadata.Namespace)
		if serviceName == "" || namespace == "" {
			continue
		}
		key := namespace + "/" + serviceName
		value := health[key]
		for _, endpoint := range item.Endpoints {
			if len(endpoint.Addresses) == 0 {
				continue
			}
			value.Total++
			ready := endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready
			terminating := endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating
			if ready && !terminating {
				value.Ready++
			}
		}
		health[key] = value
	}
	return health, nil
}

func applyKubernetesEndpointHealth(services []KubernetesService, health map[string]kubernetesEndpointHealth) {
	for index := range services {
		services[index].EndpointHealthKnown = true
		value := health[services[index].Namespace+"/"+services[index].Name]
		services[index].ReadyEndpoints = value.Ready
		services[index].TotalEndpoints = value.Total
	}
}

func decodeKubernetesServices(b []byte) ([]KubernetesService, error) {
	var response struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Type  string `json:"type"`
				Ports []struct {
					Name        string `json:"name"`
					Port        int    `json:"port"`
					AppProtocol string `json:"appProtocol"`
				} `json:"ports"`
			} `json:"spec"`
			Status struct {
				LoadBalancer struct {
					Ingress []struct {
						Hostname string `json:"hostname"`
						IP       string `json:"ip"`
					} `json:"ingress"`
				} `json:"loadBalancer"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &response); err != nil {
		return nil, err
	}
	services := make([]KubernetesService, 0, len(response.Items))
	for _, item := range response.Items {
		if strings.TrimSpace(item.Metadata.Name) == "" || strings.TrimSpace(item.Metadata.Namespace) == "" {
			continue
		}
		service := KubernetesService{
			Name: item.Metadata.Name, Namespace: item.Metadata.Namespace, Type: item.Spec.Type,
			Ports: make([]ServicePort, 0, len(item.Spec.Ports)), LoadBalancerHosts: make([]string, 0),
		}
		for _, port := range item.Spec.Ports {
			service.Ports = append(service.Ports, ServicePort{Name: port.Name, Port: port.Port, AppProtocol: port.AppProtocol})
		}
		for _, ingress := range item.Status.LoadBalancer.Ingress {
			host := strings.TrimSpace(ingress.Hostname)
			if host == "" {
				host = strings.TrimSpace(ingress.IP)
			}
			if host != "" {
				service.LoadBalancerHosts = append(service.LoadBalancerHosts, host)
			}
		}
		services = append(services, service)
	}
	sort.Slice(services, func(i, j int) bool {
		if services[i].Namespace == services[j].Namespace {
			return services[i].Name < services[j].Name
		}
		return services[i].Namespace < services[j].Namespace
	})
	return services, nil
}

func (s *Service) collectTerraformOutputs(ctx context.Context, project, name string, report *Report) {
	if s.outputProvider != nil {
		if outputs, err := s.outputProvider.StateOutputs(ctx, project, name, "infra"); err == nil {
			copyAllowedTerraformOutputs(outputs, report)
			return
		}
	}
	stage := filepath.Base(s.config.Paths.TerraformInfraDir)
	dataDir := filepath.Join(s.config.Paths.DataDir, "terraform", name, stage)
	b, err := s.capture(ctx, s.config.Paths.TerraformInfraDir, s.config.Tools.Terraform, []string{"output", "-json", "-no-color"}, "", "TF_DATA_DIR="+dataDir, "TF_WORKSPACE="+name)
	if err != nil {
		return
	}
	var outputs map[string]struct {
		Value any `json:"value"`
	}
	if json.Unmarshal(b, &outputs) != nil {
		return
	}
	flattened := make(map[string]any, len(outputs))
	for key, value := range outputs {
		flattened[key] = value.Value
	}
	copyAllowedTerraformOutputs(flattened, report)
}

func copyAllowedTerraformOutputs(outputs map[string]any, report *Report) {
	allowed := map[string]bool{
		"rds_endpoint": true, "aurora_writer_endpoint": true,
		"aurora_reader_endpoint": true, "elasticache_configuration_endpoint": true,
		"elasticache_reader_endpoint": true, "postgres_endpoint": true,
		"documentdb_endpoint": true, "documentdb_reader_endpoint": true,
		"msk_bootstrap_brokers": true, "amazon_mq_endpoint": true, "amazon_mq_console_url": true,
		"ecr_repository_urls": true, "platform_backup_bucket": true,
	}
	for key, value := range outputs {
		if allowed[key] && value != nil {
			report.Outputs[key] = value
		}
	}
}

func (s *Service) componentStatuses(doc environment.Document, addons map[string]string, releases map[string]HelmRelease, services []KubernetesService, outputs map[string]any) []Component {
	result := make([]Component, 0, len(s.config.Components))
	configuredKeys := make(map[string]struct{}, len(s.config.Components))
	for _, config := range s.config.Components {
		configuredKeys[config.Key] = struct{}{}
		value, _ := environment.GetPath(doc, config.ConfigPath)
		desired, _ := value.(bool)
		component := Component{
			Key: config.Key, DisplayName: config.DisplayName, Category: config.Category,
			Desired: desired, Status: "disabled",
		}
		helmDegraded := false
		switch config.StatusType {
		case "eks_addon":
			addonStatus, installed := addons[config.StatusName]
			component.Actual = installed && addonStatus == "ACTIVE"
			if installed {
				component.Detail = "EKS add-on status: " + addonStatus
			}
		case "helm":
			releaseName := config.StatusName
			if configured, ok := environment.GetPath(doc, "components.catalog."+config.Key+".release_name"); ok {
				if value, valid := configured.(string); valid && strings.TrimSpace(value) != "" {
					releaseName = strings.TrimSpace(value)
				}
			}
			if release, ok := releases[releaseName]; ok {
				component.Actual = strings.EqualFold(release.Status, "deployed")
				component.Detail = release.Namespace + " · " + release.Chart
				if !component.Actual {
					component.Detail += " · Helm " + release.Status
					if service, healthy := healthyCatalogService(doc, config.Key, release.Namespace, services); healthy {
						component.Actual = true
						helmDegraded = true
						component.Detail += fmt.Sprintf(" · Service %d/%d 就绪", service.ReadyEndpoints, service.TotalEndpoints)
					}
				}
			}
		case "terraform":
			value, ok := outputs[config.StatusName]
			component.Actual = ok && value != nil
			if component.Actual {
				component.Detail = "Terraform output available"
			}
		}
		switch {
		case desired && component.Actual && helmDegraded:
			component.Status = "degraded"
		case desired && component.Actual:
			component.Status = "healthy"
		case desired && !component.Actual:
			component.Status = "missing"
		case !desired && component.Actual:
			component.Status = "drift"
		default:
			component.Status = "disabled"
		}
		result = append(result, component)
	}

	// Platform administrators can extend the Helm catalog without changing the
	// binary configuration. Include those environment-level catalog entries in
	// the observed component list so a successfully deployed extension appears
	// in the overview just like a built-in component.
	catalogValue, _ := environment.GetPath(doc, "components.catalog")
	catalog, _ := catalogValue.(map[string]any)
	dynamicKeys := make([]string, 0, len(catalog))
	for key := range catalog {
		if _, builtIn := configuredKeys[key]; !builtIn {
			dynamicKeys = append(dynamicKeys, key)
		}
	}
	sort.Strings(dynamicKeys)
	for _, key := range dynamicKeys {
		config, ok := catalog[key].(map[string]any)
		if !ok {
			continue
		}
		desired, _ := config["enabled"].(bool)
		displayName, _ := config["display_name"].(string)
		if strings.TrimSpace(displayName) == "" {
			displayName = key
		}
		category, _ := config["category"].(string)
		if strings.TrimSpace(category) == "" {
			category = "扩展组件"
		}
		releaseName, _ := config["release_name"].(string)
		if strings.TrimSpace(releaseName) == "" {
			releaseName = key
		}
		component := Component{Key: key, DisplayName: displayName, Category: category, Desired: desired, Status: "disabled"}
		helmDegraded := false
		if release, installed := releases[strings.TrimSpace(releaseName)]; installed {
			component.Actual = strings.EqualFold(release.Status, "deployed")
			component.Detail = release.Namespace + " · " + release.Chart
			if !component.Actual {
				component.Detail += " · Helm " + release.Status
				if service, healthy := healthyCatalogService(doc, key, release.Namespace, services); healthy {
					component.Actual = true
					helmDegraded = true
					component.Detail += fmt.Sprintf(" · Service %d/%d 就绪", service.ReadyEndpoints, service.TotalEndpoints)
				}
			}
		}
		switch {
		case component.Desired && component.Actual && helmDegraded:
			component.Status = "degraded"
		case component.Desired && component.Actual:
			component.Status = "healthy"
		case component.Desired:
			component.Status = "missing"
		case component.Actual:
			component.Status = "drift"
		}
		result = append(result, component)
	}
	collectorEnabled, _ := environment.GetPath(doc, "components.catalog.opentelemetry_collector.enabled")
	elasticsearchEnabled, _ := environment.GetPath(doc, "components.catalog.opentelemetry_collector.values.elasticsearch.enabled")
	release, releaseInstalled := releases["otel-elasticsearch"]
	dedicatedElasticsearchDesired := collectorEnabled == true && elasticsearchEnabled == true
	if dedicatedElasticsearchDesired || releaseInstalled {
		component := Component{
			Key: "otel_elasticsearch", DisplayName: "OpenTelemetry Elasticsearch", Category: "监控",
			Desired: dedicatedElasticsearchDesired, Status: "missing",
		}
		if releaseInstalled {
			component.Actual = strings.EqualFold(release.Status, "deployed")
			component.Detail = release.Namespace + " · " + release.Chart
			if component.Actual {
				for _, service := range services {
					if service.Namespace == release.Namespace && service.Name == "otel-elasticsearch" && service.EndpointHealthKnown {
						component.Actual = service.ReadyEndpoints > 0
						component.Detail += fmt.Sprintf(" · Service %d/%d 就绪", service.ReadyEndpoints, service.TotalEndpoints)
						break
					}
				}
			}
		}
		switch {
		case component.Desired && component.Actual:
			component.Status = "healthy"
		case component.Desired:
			component.Status = "missing"
		case component.Actual:
			component.Status = "drift"
		default:
			component.Status = "degraded"
		}
		result = append(result, component)
	}
	return result
}

// healthyCatalogService prevents a failed Helm metadata revision from being
// presented as an absent component while its existing workload is still
// serving traffic. A Service is accepted only when EndpointSlice collection
// positively confirms at least one ready endpoint; mere object existence is
// not enough to hide a failed or interrupted installation.
func healthyCatalogService(doc environment.Document, key, releaseNamespace string, services []KubernetesService) (KubernetesService, bool) {
	prefix := "components.catalog." + key
	serviceName := strings.TrimSpace(stringAt(doc, prefix+".service_name"))
	namespace := strings.TrimSpace(stringAt(doc, prefix+".namespace"))
	if namespace == "" {
		namespace = strings.TrimSpace(releaseNamespace)
	}
	if serviceName == "" || namespace == "" {
		return KubernetesService{}, false
	}
	for _, service := range services {
		if service.Namespace == namespace && service.Name == serviceName && service.EndpointHealthKnown && service.ReadyEndpoints > 0 {
			return service, true
		}
	}
	return KubernetesService{}, false
}

// kubernetesContext prepares a request-scoped kubeconfig while reusing a
// short-lived, immutable kubeconfig snapshot. AWS credentials are still loaded
// for every request and are never stored in the cache.
func (s *Service) kubernetesContext(ctx context.Context, name string) (context.Context, environment.Document, string, error) {
	doc, err := s.environments.Load(name)
	if err != nil {
		return ctx, nil, "", err
	}
	doc = environment.ApplyDefaults(doc, stringAt(doc, "project"), stringAt(doc, "environment"))
	project, region, clusterName := stringAt(doc, "project"), stringAt(doc, "region"), environment.ClusterName(doc)
	if s.awsProvider == nil {
		return ctx, nil, "", errors.New("当前项目未绑定 AWS 凭据，无法访问 EKS")
	}
	awsEnvironment, err := s.awsProvider.Environment(ctx, project)
	if err != nil || len(awsEnvironment) == 0 {
		return ctx, nil, "", errors.New("当前项目未绑定可用的 AWS 凭据，无法访问 EKS")
	}
	commandContext := context.WithValue(ctx, awsEnvironmentContextKey{}, awsEnvironment)
	resourceKey := strings.Join([]string{"cluster-access", project, region, clusterName}, "\x00")
	snapshot, err := loadOperationalValue(commandContext, s.operations, operationalCacheKey(name, resourceKey), 45*time.Second, func() ([]byte, error) {
		clusterStatus, captureErr := s.capture(commandContext, "", s.config.Tools.AWS, []string{
			"eks", "describe-cluster", "--region", region, "--name", clusterName,
			"--query", "cluster.status", "--output", "text",
		}, "")
		if captureErr != nil {
			return nil, errors.New("无法确认当前环境 EKS 集群状态，请检查集群名称、Region 和项目 AWS 权限")
		}
		if !strings.EqualFold(strings.TrimSpace(string(clusterStatus)), "ACTIVE") {
			return nil, fmt.Errorf("当前 EKS 集群尚未就绪，状态为 %s", safeStatusValue(string(clusterStatus)))
		}
		sourcePath, updateErr := s.updateKubeconfig(commandContext, name, region, clusterName)
		if updateErr != nil {
			return nil, errors.New("EKS 已就绪，但当前项目 AWS 身份无法生成 kubeconfig")
		}
		defer os.Remove(sourcePath)
		info, statErr := os.Stat(sourcePath)
		if statErr != nil {
			return nil, errors.New("生成的 EKS kubeconfig 无法读取")
		}
		if info.Size() <= 0 || info.Size() > 1024*1024 {
			return nil, errors.New("生成的 EKS kubeconfig 大小异常")
		}
		content, readErr := os.ReadFile(sourcePath) // #nosec G304 -- path is created by updateKubeconfig inside the private runtime directory.
		if readErr != nil {
			return nil, errors.New("生成的 EKS kubeconfig 无法读取")
		}
		return append([]byte(nil), content...), nil
	})
	if err != nil {
		return ctx, nil, "", err
	}
	kubeconfig, err := s.writeKubeconfigSnapshot(snapshot)
	if err != nil {
		return ctx, nil, "", errors.New("无法创建隔离的 EKS kubeconfig")
	}
	return commandContext, doc, kubeconfig, nil
}

func (s *Service) writeKubeconfigSnapshot(content []byte) (string, error) {
	dir := filepath.Join(s.config.Paths.DataDir, "kubeconfigs", "status")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "eks-request-*.yaml")
	if err != nil {
		return "", err
	}
	path := file.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		_ = file.Close()
		err = chmodErr
		return "", err
	}
	if _, writeErr := file.Write(content); writeErr != nil {
		_ = file.Close()
		err = writeErr
		return "", err
	}
	if closeErr := file.Close(); closeErr != nil {
		err = closeErr
		return "", err
	}
	return path, nil
}

func (s *Service) acquireCommandSlot(ctx context.Context) error {
	if s.commandSlots == nil {
		return nil
	}
	select {
	case s.commandSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseCommandSlot() {
	if s.commandSlots != nil {
		<-s.commandSlots
	}
}

func cloneKubernetesServices(source []KubernetesService) []KubernetesService {
	result := append([]KubernetesService(nil), source...)
	for index := range result {
		result[index].Ports = append([]ServicePort(nil), source[index].Ports...)
		result[index].LoadBalancerHosts = append([]string(nil), source[index].LoadBalancerHosts...)
	}
	return result
}

func (s *Service) updateKubeconfig(ctx context.Context, name, region, cluster string) (string, error) {
	dir := filepath.Join(s.config.Paths.DataDir, "kubeconfigs", "status")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "eks-status-*.yaml")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	// update-kubeconfig creates the target itself. Removing the empty file also
	// avoids asking AWS CLI to merge an empty YAML document.
	if err := os.Remove(path); err != nil {
		return "", err
	}
	_, err = s.capture(ctx, "", s.config.Tools.AWS, []string{
		"eks", "update-kubeconfig", "--region", region, "--name", cluster,
		"--alias", cluster, "--kubeconfig", path,
	}, path)
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (s *Service) capture(ctx context.Context, dir, name string, args []string, kubeconfig string, extraEnv ...string) ([]byte, error) {
	if err := s.acquireCommandSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseCommandSlot()
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, name, args...) // #nosec G204 -- executable is an administrator-configured tool and arguments are passed directly without a shell.
	cmd.Dir = dir
	env := removeEnvironmentKeys(os.Environ(), "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_DEFAULT_PROFILE")
	projectEnvironment, hasProjectCredential := ctx.Value(awsEnvironmentContextKey{}).([]string)
	if hasProjectCredential && len(projectEnvironment) > 0 {
		env = append(env, projectEnvironment...)
	}
	if kubeconfig != "" {
		env = append(env, "KUBECONFIG="+kubeconfig)
	}
	env = append(env, extraEnv...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func removeEnvironmentKeys(source []string, keys ...string) []string {
	blocked := make(map[string]bool, len(keys))
	for _, key := range keys {
		blocked[key] = true
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

func safeStatusValue(raw string) string {
	value := strings.ToUpper(strings.TrimSpace(raw))
	if value == "" || len(value) > 32 {
		return "UNKNOWN"
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && character != '_' {
			return "UNKNOWN"
		}
	}
	return value
}

func stringAt(doc environment.Document, path string) string {
	value, _ := environment.GetPath(doc, path)
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
