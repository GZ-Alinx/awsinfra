package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/environment"
)

// ApplicationTopology is a read-only, project-scoped view of the workloads
// already running in Kubernetes. It deliberately contains no Secret data.
type ApplicationTopology struct {
	ObservedAt time.Time          `json:"observed_at"`
	Source     TopologySource     `json:"source"`
	Summary    TopologySummary    `json:"summary"`
	Nodes      []ApplicationNode  `json:"nodes"`
	Edges      []ApplicationEdge  `json:"edges"`
	Alerts     []ApplicationAlert `json:"alerts"`
	Warnings   []string           `json:"warnings"`
}

type TopologySource struct {
	Kubernetes       bool   `json:"kubernetes"`
	Prometheus       bool   `json:"prometheus"`
	RuntimeGraph     bool   `json:"runtime_graph"`
	Detail           string `json:"detail"`
	ConnectionDetail string `json:"connection_detail"`
}

type TopologySummary struct {
	Normal              int `json:"normal"`
	Warning             int `json:"warning"`
	Abnormal            int `json:"abnormal"`
	Total               int `json:"total"`
	Connections         int `json:"connections"`
	RuntimeConnections  int `json:"runtime_connections"`
	EndpointConnections int `json:"endpoint_connections"`
	DeclaredConnections int `json:"declared_connections"`
}

type ApplicationNode struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	Kind            string            `json:"kind"`
	Layer           string            `json:"layer"`
	State           string            `json:"state"`
	StateReason     string            `json:"state_reason"`
	DesiredReplicas int               `json:"desired_replicas,omitempty"`
	ReadyReplicas   int               `json:"ready_replicas,omitempty"`
	Pods            int               `json:"pods,omitempty"`
	ReadyPods       int               `json:"ready_pods,omitempty"`
	Restarts        int               `json:"restarts,omitempty"`
	CPUCores        float64           `json:"cpu_cores,omitempty"`
	MemoryBytes     float64           `json:"memory_bytes,omitempty"`
	ReadyEndpoints  int               `json:"ready_endpoints,omitempty"`
	TotalEndpoints  int               `json:"total_endpoints,omitempty"`
	Services        []string          `json:"services"`
	Ports           []ServicePort     `json:"ports"`
	Hosts           []string          `json:"hosts"`
	Labels          map[string]string `json:"labels,omitempty"`
}

type ApplicationEdge struct {
	ID             string  `json:"id"`
	Source         string  `json:"source"`
	Target         string  `json:"target"`
	Relation       string  `json:"relation"`
	Protocol       string  `json:"protocol,omitempty"`
	Label          string  `json:"label,omitempty"`
	Evidence       string  `json:"evidence"`
	Verified       bool    `json:"verified"`
	State          string  `json:"state"`
	ReadyEndpoints int     `json:"ready_endpoints,omitempty"`
	TotalEndpoints int     `json:"total_endpoints,omitempty"`
	RequestRate    float64 `json:"request_rate,omitempty"`
}

type ApplicationAlert struct {
	Name        string `json:"name"`
	Severity    string `json:"severity"`
	State       string `json:"state"`
	Namespace   string `json:"namespace,omitempty"`
	Workload    string `json:"workload,omitempty"`
	Pod         string `json:"pod,omitempty"`
	Service     string `json:"service,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
}

type topologySnapshot struct {
	Items []topologyItem `json:"items"`
}

type topologyItem struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		Labels            map[string]string `json:"labels"`
		Annotations       map[string]string `json:"annotations"`
		OwnerReferences   []ownerReference  `json:"ownerReferences"`
		CreationTimestamp string            `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int             `json:"replicas"`
		Selector topologySelector `json:"selector"`
		Template struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec topologyPodSpec `json:"spec"`
		} `json:"template"`
		IngressClassName string `json:"ingressClassName"`
		Ports            []struct {
			Name        string `json:"name"`
			Port        int    `json:"port"`
			TargetPort  any    `json:"targetPort"`
			AppProtocol string `json:"appProtocol"`
		} `json:"ports"`
		Rules []struct {
			Host string `json:"host"`
			HTTP struct {
				Paths []struct {
					Path    string `json:"path"`
					Backend struct {
						Service struct {
							Name string `json:"name"`
							Port struct {
								Name   string `json:"name"`
								Number int    `json:"number"`
							} `json:"port"`
						} `json:"service"`
					} `json:"backend"`
				} `json:"paths"`
			} `json:"http"`
		} `json:"rules"`
	} `json:"spec"`
	Data   map[string]string `json:"data"`
	Status struct {
		Replicas               int    `json:"replicas"`
		ReadyReplicas          int    `json:"readyReplicas"`
		AvailableReplicas      int    `json:"availableReplicas"`
		CurrentReplicas        int    `json:"currentReplicas"`
		NumberReady            int    `json:"numberReady"`
		DesiredNumberScheduled int    `json:"desiredNumberScheduled"`
		Phase                  string `json:"phase"`
		ContainerStatuses      []struct {
			Name         string `json:"name"`
			Ready        bool   `json:"ready"`
			RestartCount int    `json:"restartCount"`
			State        struct {
				Waiting *struct {
					Reason string `json:"reason"`
				} `json:"waiting"`
				Terminated *struct {
					Reason   string `json:"reason"`
					ExitCode int    `json:"exitCode"`
				} `json:"terminated"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

type topologyPodSpec struct {
	Containers []struct {
		Name    string   `json:"name"`
		Command []string `json:"command"`
		Args    []string `json:"args"`
		Env     []struct {
			Name      string `json:"name"`
			Value     string `json:"value"`
			ValueFrom struct {
				ConfigMapKeyRef *struct {
					Name string `json:"name"`
					Key  string `json:"key"`
				} `json:"configMapKeyRef"`
			} `json:"valueFrom"`
		} `json:"env"`
		EnvFrom []struct {
			ConfigMapRef *struct {
				Name string `json:"name"`
			} `json:"configMapRef"`
		} `json:"envFrom"`
	} `json:"containers"`
	Volumes []struct {
		ConfigMap *struct {
			Name string `json:"name"`
		} `json:"configMap"`
	} `json:"volumes"`
}

type ownerReference struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Controller *bool  `json:"controller"`
}

// topologySelector accepts both the Service shape ({app: api}) and the
// workload LabelSelector shape ({matchLabels: {app: api}}).
type topologySelector map[string]string

func (s *topologySelector) UnmarshalJSON(payload []byte) error {
	var direct map[string]string
	if err := json.Unmarshal(payload, &direct); err == nil {
		*s = direct
		return nil
	}
	var workload struct {
		MatchLabels map[string]string `json:"matchLabels"`
	}
	if err := json.Unmarshal(payload, &workload); err != nil {
		return err
	}
	*s = workload.MatchLabels
	return nil
}

type topologyWorkload struct {
	node           ApplicationNode
	selector       map[string]string
	podNames       map[string]struct{}
	unhealthyPods  int
	dependencyText string
}

type topologyService struct {
	name      string
	namespace string
	selector  map[string]string
	ports     []ServicePort
}

type topologyEndpointRelation struct {
	ready     int
	total     int
	workloads map[string]kubernetesEndpointHealth
}

type runtimeTopologyConnection struct {
	sourceName      string
	sourceNamespace string
	targetName      string
	targetNamespace string
	protocol        string
	evidence        string
	requestRate     float64
}

type prometheusVector struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// ApplicationTopology returns a short-lived, coalesced observation. A normal
// page refresh therefore does not fan out into repeated kubectl/Prometheus
// calls when several operators open the same project environment.
func (s *Service) ApplicationTopology(ctx context.Context, name string, fresh bool) (*ApplicationTopology, error) {
	resource := "application-topology"
	if fresh {
		s.operations.delete(name, resource)
	}
	payload, err := loadOperationalValue(ctx, s.operations, operationalCacheKey(name, resource), 45*time.Second, func() ([]byte, error) {
		topology, loadErr := s.collectApplicationTopology(ctx, name)
		if loadErr != nil {
			return nil, loadErr
		}
		return json.Marshal(topology)
	})
	if err != nil {
		return nil, err
	}
	var topology ApplicationTopology
	if err := json.Unmarshal(payload, &topology); err != nil {
		return nil, err
	}
	return &topology, nil
}

func (s *Service) collectApplicationTopology(ctx context.Context, name string) (*ApplicationTopology, error) {
	commandContext, doc, kubeconfig, err := s.kubernetesContext(ctx, name)
	if err != nil {
		return nil, err
	}
	defer os.Remove(kubeconfig)

	resourceTypes := "deployments.apps,statefulsets.apps,daemonsets.apps,replicasets.apps,services,ingresses.networking.k8s.io,pods,configmaps"
	// One multi-resource request reuses the same EKS authentication round-trip
	// for topology and EndpointSlice data. Existing clusters without
	// EndpointSlice permission transparently fall back to the base resource set.
	workloadPayload, endpointErr := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{
		"get", resourceTypes + ",endpointslices.discovery.k8s.io", "-A", "-o", "json",
	}, kubeconfig)
	endpointPayload := workloadPayload
	if endpointErr != nil {
		workloadPayload, err = s.capture(commandContext, "", s.config.Tools.Kubectl, []string{
			"get", resourceTypes, "-A", "-o", "json",
		}, kubeconfig)
		if err != nil {
			return nil, errors.New("扫描 EKS 应用资源失败，请检查 Kubernetes API 连通性、EKS Access Entry 和跨 Namespace 只读权限")
		}
		endpointPayload = nil
	}

	namespaces := topologyNamespaces(doc)
	topology, services, podOwners, err := decodeApplicationTopology(workloadPayload, namespaces)
	if err != nil {
		return nil, errors.New("EKS 返回的应用资源数据格式无效")
	}
	topology.ObservedAt = time.Now().UTC()
	topology.Source = TopologySource{
		Kubernetes:       true,
		Detail:           "工作负载、Service、Ingress 与就绪状态来自 Kubernetes API",
		ConnectionDetail: "Ingress 路由和 Service 选择器来自 Kubernetes 配置",
	}
	topology.Warnings = make([]string, 0)

	if endpointErr == nil {
		if relations, decodeErr := decodeTopologyEndpointRelations(endpointPayload, podOwners); decodeErr == nil {
			applyTopologyEndpointRelations(topology, services, relations)
			topology.Source.ConnectionDetail = "Ingress 路由来自 Kubernetes 配置；Service 到工作负载的连线已由 EndpointSlice 实际端点验证"
		} else {
			topology.Warnings = append(topology.Warnings, "EndpointSlice 数据格式无效；Service 连线仅展示 Kubernetes 配置关系")
		}
	} else {
		topology.Warnings = append(topology.Warnings, "当前身份无法读取 EndpointSlice；Service 连线仅展示 Kubernetes 配置关系")
	}

	prometheusService, found := selectPrometheusService(services)
	if found {
		metrics, alerts, queryWarnings := s.collectPrometheusTopologySignals(commandContext, kubeconfig, prometheusService, namespaces)
		topology.Warnings = append(topology.Warnings, queryWarnings...)
		if metrics.available {
			topology.Source.Prometheus = true
			topology.Source.Detail = "拓扑与就绪状态来自 Kubernetes API，资源指标和活动告警来自 Prometheus"
			applyPrometheusMetrics(topology, podOwners, metrics)
			applyPrometheusAlerts(topology, alerts)
			if applyRuntimeTopologyConnections(topology, metrics.connections) > 0 {
				topology.Source.RuntimeGraph = true
				topology.Source.ConnectionDetail += "；应用调用连线来自 Prometheus 中最近 5 分钟的实际请求指标"
			}
			topology.Alerts = alerts
		}
	} else {
		topology.Warnings = append(topology.Warnings, "当前环境未发现可查询的 Prometheus Service；页面仍展示 Kubernetes 实时拓扑与就绪状态")
	}
	applyTopologyEdgeStates(topology)
	summarizeTopology(topology)
	return topology, nil
}

func topologyNamespaces(doc environment.Document) map[string]struct{} {
	result := make(map[string]struct{})
	if raw, found := environment.GetPath(doc, "namespaces"); found {
		if values, ok := raw.(map[string]any); ok {
			for namespace := range values {
				if namespace = strings.TrimSpace(namespace); namespace != "" {
					result[namespace] = struct{}{}
				}
			}
		}
	}
	if raw, found := environment.GetPath(doc, "components.catalog"); found {
		if catalog, ok := raw.(map[string]any); ok {
			for _, value := range catalog {
				component, valid := value.(map[string]any)
				if !valid || !mapBool(component, "enabled") {
					continue
				}
				if namespace, _ := component["namespace"].(string); strings.TrimSpace(namespace) != "" {
					result[strings.TrimSpace(namespace)] = struct{}{}
				}
			}
		}
	}
	return result
}

func mapBool(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func decodeApplicationTopology(payload []byte, allowedNamespaces map[string]struct{}) (*ApplicationTopology, []topologyService, map[string]string, error) {
	var snapshot topologySnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil, nil, nil, err
	}
	includeNamespace := func(namespace string) bool {
		if len(allowedNamespaces) == 0 {
			return !isSystemNamespace(namespace)
		}
		_, ok := allowedNamespaces[namespace]
		return ok
	}
	configMaps := make(map[string]map[string]string)
	for _, item := range snapshot.Items {
		if item.Kind != "ConfigMap" || !includeNamespace(item.Metadata.Namespace) {
			continue
		}
		configMaps[item.Metadata.Namespace+"/"+item.Metadata.Name] = item.Data
	}

	replicaSetOwners := make(map[string]string)
	for _, item := range snapshot.Items {
		if item.Kind != "ReplicaSet" || !includeNamespace(item.Metadata.Namespace) {
			continue
		}
		if owner, ok := controllerOwner(item.Metadata.OwnerReferences); ok && owner.Kind == "Deployment" {
			replicaSetOwners[item.Metadata.Namespace+"/"+item.Metadata.Name] = owner.Name
		}
	}

	workloads := make(map[string]*topologyWorkload)
	services := make([]topologyService, 0)
	podOwners := make(map[string]string)
	for _, item := range snapshot.Items {
		namespace := strings.TrimSpace(item.Metadata.Namespace)
		if !includeNamespace(namespace) {
			continue
		}
		switch item.Kind {
		case "Deployment", "StatefulSet", "DaemonSet":
			node := workloadNode(item)
			workloads[node.ID] = &topologyWorkload{
				node: node, selector: cloneStringMap(map[string]string(item.Spec.Selector)), podNames: make(map[string]struct{}),
				dependencyText: topologyDependencyText(item, configMaps),
			}
		case "Service":
			service := topologyService{name: item.Metadata.Name, namespace: namespace, selector: cloneStringMap(map[string]string(item.Spec.Selector))}
			for _, port := range item.Spec.Ports {
				service.ports = append(service.ports, ServicePort{Name: port.Name, Port: port.Port, AppProtocol: port.AppProtocol})
			}
			services = append(services, service)
		}
	}

	for _, item := range snapshot.Items {
		namespace := strings.TrimSpace(item.Metadata.Namespace)
		if item.Kind != "Pod" || !includeNamespace(namespace) {
			continue
		}
		owner, ok := controllerOwner(item.Metadata.OwnerReferences)
		if !ok {
			continue
		}
		workloadKind, workloadName := owner.Kind, owner.Name
		if owner.Kind == "ReplicaSet" {
			if deployment, found := replicaSetOwners[namespace+"/"+owner.Name]; found {
				workloadKind, workloadName = "Deployment", deployment
			}
		}
		id := workloadNodeID(namespace, workloadKind, workloadName)
		workload, found := workloads[id]
		if !found {
			continue
		}
		workload.podNames[item.Metadata.Name] = struct{}{}
		podOwners[namespace+"/"+item.Metadata.Name] = id
		workload.node.Pods++
		podReady := len(item.Status.ContainerStatuses) > 0 && strings.EqualFold(item.Status.Phase, "Running")
		for _, container := range item.Status.ContainerStatuses {
			workload.node.Restarts += container.RestartCount
			podReady = podReady && container.Ready
			if container.State.Waiting != nil {
				switch container.State.Waiting.Reason {
				case "CrashLoopBackOff", "CreateContainerError", "CreateContainerConfigError", "ErrImagePull", "ImagePullBackOff":
					workload.unhealthyPods++
					workload.node.State = "abnormal"
					workload.node.StateReason = "Pod " + item.Metadata.Name + "：" + container.State.Waiting.Reason
				}
			}
		}
		if podReady {
			workload.node.ReadyPods++
		}
	}

	topology := &ApplicationTopology{
		Nodes:  make([]ApplicationNode, 0, len(workloads)),
		Edges:  make([]ApplicationEdge, 0),
		Alerts: make([]ApplicationAlert, 0),
	}
	workloadIDs := make([]string, 0, len(workloads))
	for id := range workloads {
		workloadIDs = append(workloadIDs, id)
	}
	sort.Strings(workloadIDs)
	serviceTargets := make(map[string][]string)
	serviceByKey := make(map[string]topologyService, len(services))
	serviceNodes := make(map[string]ApplicationNode, len(services))
	for _, service := range services {
		serviceKey := service.namespace + "/" + service.name
		serviceNodeID := "service:" + service.namespace + ":" + service.name
		serviceByKey[serviceKey] = service
		layer := classifyTopologyLayer(service.namespace, service.name)
		for _, id := range workloadIDs {
			workload := workloads[id]
			if workload.node.Namespace == service.namespace && selectorMatches(service.selector, workload.selector) {
				workload.node.Services = append(workload.node.Services, service.name)
				workload.node.Ports = mergeServicePorts(workload.node.Ports, service.ports)
				serviceTargets[serviceKey] = append(serviceTargets[serviceKey], id)
				layer = workload.node.Layer
				topology.Edges = append(topology.Edges, ApplicationEdge{
					ID: serviceNodeID + "->" + id, Source: serviceNodeID, Target: id,
					Relation: "service_selector", Label: "选择器", Evidence: "Kubernetes Service selector",
					State: "normal",
				})
			}
		}
		serviceNodes[serviceNodeID] = ApplicationNode{
			ID: serviceNodeID, Name: service.name, Namespace: service.namespace,
			Kind: "Service", Layer: layer, State: "normal",
			StateReason: "Service 已存在，等待 EndpointSlice 验证实际后端",
			Services:    []string{service.name}, Ports: append([]ServicePort(nil), service.ports...),
			Hosts: make([]string, 0),
		}
	}
	for _, workloadID := range workloadIDs {
		workload := workloads[workloadID]
		if workload.node.Layer != "application" || strings.TrimSpace(workload.dependencyText) == "" {
			continue
		}
		for _, service := range services {
			if classifyTopologyLayer(service.namespace, service.name) != "data" ||
				!serviceDependencyReference(workload.dependencyText, workload.node.Namespace, service) {
				continue
			}
			serviceNodeID := "service:" + service.namespace + ":" + service.name
			topology.Edges = append(topology.Edges, ApplicationEdge{
				ID:     workloadID + "->dependency:" + serviceNodeID,
				Source: workloadID, Target: serviceNodeID, Relation: "declared_dependency",
				Protocol: dependencyProtocol(service), Label: "配置依赖",
				Evidence: "工作负载明文环境变量、启动参数或所引用 ConfigMap 中匹配到 Kubernetes Service",
				Verified: false, State: "normal",
			})
		}
	}

	accessNodes := make(map[string]*ApplicationNode)
	domainNodes := make(map[string]*ApplicationNode)
	entryOrder := make([]string, 0)
	entryEdges := make(map[string]struct{})
	for _, item := range snapshot.Items {
		namespace := strings.TrimSpace(item.Metadata.Namespace)
		if item.Kind != "Ingress" || !includeNamespace(namespace) {
			continue
		}
		className := strings.TrimSpace(item.Spec.IngressClassName)
		if className == "" {
			className = strings.TrimSpace(item.Metadata.Annotations["kubernetes.io/ingress.class"])
		}
		if className == "" {
			className = "Ingress Gateway"
		}
		accessID := "gateway:" + namespace + ":" + strings.ToLower(strings.ReplaceAll(className, " ", "-"))
		if _, exists := accessNodes[accessID]; !exists {
			accessNodes[accessID] = &ApplicationNode{
				ID: accessID, Name: className, Namespace: namespace, Kind: "Gateway", Layer: "entry",
				State: "normal", StateReason: "IngressClass 访问入口已配置",
				Services: make([]string, 0), Ports: make([]ServicePort, 0), Hosts: make([]string, 0),
			}
			entryOrder = append(entryOrder, accessID)
		}
		for _, rule := range item.Spec.Rules {
			host := strings.TrimSpace(rule.Host)
			if host == "" {
				host = "*"
			}
			// Several Ingress objects may deliberately share one host and split
			// its paths. Present one domain node per namespace/host and connect it
			// to the configured gateway so the access chain stays explicit.
			domainID := "domain:" + namespace + ":" + host
			domain, exists := domainNodes[domainID]
			if !exists {
				domain = &ApplicationNode{
					ID: domainID, Name: host, Namespace: namespace, Kind: "Domain", Layer: "entry",
					State: "normal", StateReason: "Ingress 路由已存在", Hosts: []string{host},
					Services: make([]string, 0), Ports: make([]ServicePort, 0),
				}
				domainNodes[domainID] = domain
				entryOrder = append(entryOrder, domainID)
			}
			accessEdgeID := accessID + "->" + domainID
			if _, exists := entryEdges[accessEdgeID]; !exists {
				topology.Edges = append(topology.Edges, ApplicationEdge{
					ID: accessEdgeID, Source: accessID, Target: domainID,
					Relation: "exposes_domain", Protocol: "HTTPS", Label: host,
					Evidence: "Kubernetes IngressClass 与 host 配置", Verified: true, State: "normal",
				})
				entryEdges[accessEdgeID] = struct{}{}
				accessNodes[accessID].Hosts = appendUnique(accessNodes[accessID].Hosts, host)
			}
			for _, path := range rule.HTTP.Paths {
				serviceKey := namespace + "/" + path.Backend.Service.Name
				domain.Services = appendUnique(domain.Services, path.Backend.Service.Name)
				service, exists := serviceByKey[serviceKey]
				if !exists {
					domain.State = worseState(domain.State, "warning")
					domain.StateReason = "后端 Service 不存在或不在当前观测范围"
					continue
				}
				serviceNodeID := "service:" + namespace + ":" + service.name
				protocol := "HTTP"
				if path.Backend.Service.Port.Name != "" {
					protocol = strings.ToUpper(path.Backend.Service.Port.Name)
				}
				topology.Edges = append(topology.Edges, ApplicationEdge{
					ID:     domainID + ":" + item.Metadata.Name + "->" + serviceNodeID + ":" + path.Path,
					Source: domainID, Target: serviceNodeID, Relation: "ingress_route",
					Protocol: protocol, Label: defaultPath(path.Path),
					Evidence: "Kubernetes Ingress spec", Verified: true, State: "normal",
				})
				serviceNode := serviceNodes[serviceNodeID]
				serviceNode.Hosts = appendUnique(serviceNode.Hosts, host)
				serviceNodes[serviceNodeID] = serviceNode
				for _, target := range serviceTargets[serviceKey] {
					workloads[target].node.Hosts = appendUnique(workloads[target].node.Hosts, host)
				}
			}
		}
	}
	sort.Strings(entryOrder)
	for _, id := range entryOrder {
		if node, found := accessNodes[id]; found {
			topology.Nodes = append(topology.Nodes, *node)
			continue
		}
		if node, found := domainNodes[id]; found {
			topology.Nodes = append(topology.Nodes, *node)
		}
	}
	serviceNodeIDs := make([]string, 0, len(serviceNodes))
	for id := range serviceNodes {
		serviceNodeIDs = append(serviceNodeIDs, id)
	}
	sort.Strings(serviceNodeIDs)
	for _, id := range serviceNodeIDs {
		node := serviceNodes[id]
		sort.Strings(node.Hosts)
		topology.Nodes = append(topology.Nodes, node)
	}
	for _, id := range workloadIDs {
		node := workloads[id].node
		sort.Strings(node.Services)
		sort.Strings(node.Hosts)
		topology.Nodes = append(topology.Nodes, node)
	}
	sort.Slice(topology.Nodes, func(i, j int) bool {
		if topology.Nodes[i].Layer != topology.Nodes[j].Layer {
			return topologyLayerOrder(topology.Nodes[i].Layer) < topologyLayerOrder(topology.Nodes[j].Layer)
		}
		if topology.Nodes[i].Namespace != topology.Nodes[j].Namespace {
			return topology.Nodes[i].Namespace < topology.Nodes[j].Namespace
		}
		return topology.Nodes[i].Name < topology.Nodes[j].Name
	})
	return topology, services, podOwners, nil
}

func workloadNode(item topologyItem) ApplicationNode {
	desired, ready := 1, item.Status.ReadyReplicas
	if item.Spec.Replicas != nil {
		desired = *item.Spec.Replicas
	}
	switch item.Kind {
	case "DaemonSet":
		desired, ready = item.Status.DesiredNumberScheduled, item.Status.NumberReady
	case "StatefulSet":
		ready = item.Status.ReadyReplicas
	}
	state, reason := "normal", "副本全部就绪"
	switch {
	case desired == 0:
		state, reason = "warning", "工作负载当前缩容为 0"
	case ready == 0:
		state, reason = "abnormal", "没有就绪副本"
	case ready < desired:
		state, reason = "warning", fmt.Sprintf("就绪副本 %d/%d", ready, desired)
	}
	name := strings.TrimSpace(item.Metadata.Name)
	namespace := strings.TrimSpace(item.Metadata.Namespace)
	return ApplicationNode{
		ID: workloadNodeID(namespace, item.Kind, name), Name: name, Namespace: namespace,
		Kind: item.Kind, Layer: classifyTopologyLayer(namespace, name), State: state, StateReason: reason,
		DesiredReplicas: desired, ReadyReplicas: ready,
		Services: make([]string, 0), Ports: make([]ServicePort, 0), Hosts: make([]string, 0),
		Labels: topologyLabels(item.Metadata.Labels),
	}
}

func workloadNodeID(namespace, kind, name string) string {
	return strings.ToLower(kind) + ":" + namespace + ":" + name
}

// topologyDependencyText collects only non-secret workload configuration. The
// returned text never leaves the backend; it is used solely to match names of
// Kubernetes Services that already exist in the observed namespaces.
func topologyDependencyText(item topologyItem, configMaps map[string]map[string]string) string {
	values := make([]string, 0)
	appendConfigMap := func(name, key string) {
		data := configMaps[item.Metadata.Namespace+"/"+strings.TrimSpace(name)]
		if key != "" {
			if value := strings.TrimSpace(data[key]); value != "" {
				values = append(values, value)
			}
			return
		}
		keys := make([]string, 0, len(data))
		for current := range data {
			keys = append(keys, current)
		}
		sort.Strings(keys)
		for _, current := range keys {
			if value := strings.TrimSpace(data[current]); value != "" {
				values = append(values, value)
			}
		}
	}
	for _, container := range item.Spec.Template.Spec.Containers {
		values = append(values, container.Command...)
		values = append(values, container.Args...)
		for _, env := range container.Env {
			// Environment variable names are non-secret and often carry the only
			// available dependency signal when the value itself comes from a
			// Secret (for example SPRING_DATA_REDIS_PASSWORD).
			if name := strings.TrimSpace(env.Name); name != "" {
				values = append(values, name)
			}
			if value := strings.TrimSpace(env.Value); value != "" {
				values = append(values, value)
			}
			if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil {
				appendConfigMap(ref.Name, ref.Key)
			}
		}
		for _, envFrom := range container.EnvFrom {
			if envFrom.ConfigMapRef != nil {
				appendConfigMap(envFrom.ConfigMapRef.Name, "")
			}
		}
	}
	for _, volume := range item.Spec.Template.Spec.Volumes {
		if volume.ConfigMap != nil {
			appendConfigMap(volume.ConfigMap.Name, "")
		}
	}
	return strings.ToLower(strings.Join(values, "\n"))
}

func serviceDependencyReference(configuration, workloadNamespace string, service topologyService) bool {
	configuration = strings.ToLower(configuration)
	name := strings.ToLower(strings.TrimSpace(service.name))
	namespace := strings.ToLower(strings.TrimSpace(service.namespace))
	if configuration == "" || name == "" || namespace == "" {
		return false
	}
	for _, candidate := range []string{
		name + "." + namespace + ".svc.cluster.local",
		name + "." + namespace + ".svc",
		name + "." + namespace,
	} {
		if strings.Contains(configuration, candidate) {
			return true
		}
	}
	if namespace != strings.ToLower(strings.TrimSpace(workloadNamespace)) {
		return false
	}
	pattern := `(^|[^a-z0-9-])` + regexp.QuoteMeta(name) + `([^a-z0-9-]|$)`
	matched, _ := regexp.MatchString(pattern, configuration)
	if matched {
		return true
	}
	// Framework conventions can describe a dependency type without repeating
	// the Kubernetes Service name. Keep these aliases intentionally narrow and
	// only apply them to a Service in the same namespace.
	aliases := make([]string, 0)
	switch {
	case strings.Contains(name, "mysql"), strings.Contains(name, "mariadb"):
		aliases = []string{"datasource", "jdbc:mysql", "mysql"}
	case strings.Contains(name, "postgres"):
		aliases = []string{"postgres", "jdbc:postgresql"}
	case strings.Contains(name, "mongo"):
		aliases = []string{"mongodb", "mongo_uri", "mongo-url"}
	case strings.Contains(name, "redis"), strings.Contains(name, "valkey"):
		aliases = []string{"redis", "redisson"}
	case strings.Contains(name, "rabbitmq"):
		aliases = []string{"rabbitmq", "spring_rabbit", "amqp"}
	case strings.Contains(name, "activemq"):
		aliases = []string{"activemq", "spring_activemq"}
	case strings.Contains(name, "kafka"):
		aliases = []string{"kafka", "bootstrap_servers"}
	case strings.Contains(name, "etcd"):
		aliases = []string{"etcd"}
	case strings.Contains(name, "consul"):
		aliases = []string{"consul"}
	}
	for _, alias := range aliases {
		if strings.Contains(configuration, alias) {
			return true
		}
	}
	return false
}

func dependencyProtocol(service topologyService) string {
	value := strings.ToLower(service.name)
	switch {
	case strings.Contains(value, "mysql"), strings.Contains(value, "mariadb"):
		return "MySQL"
	case strings.Contains(value, "postgres"):
		return "PostgreSQL"
	case strings.Contains(value, "mongo"):
		return "MongoDB"
	case strings.Contains(value, "redis"), strings.Contains(value, "valkey"):
		return "Redis"
	case strings.Contains(value, "rabbitmq"):
		return "AMQP"
	case strings.Contains(value, "kafka"):
		return "Kafka"
	case strings.Contains(value, "etcd"):
		return "etcd"
	case strings.Contains(value, "consul"):
		return "Consul"
	case strings.Contains(value, "activemq"):
		return "ActiveMQ"
	default:
		if len(service.ports) > 0 && strings.TrimSpace(service.ports[0].AppProtocol) != "" {
			return service.ports[0].AppProtocol
		}
		return "TCP"
	}
}

func controllerOwner(owners []ownerReference) (ownerReference, bool) {
	for _, owner := range owners {
		if owner.Controller == nil || *owner.Controller {
			return owner, strings.TrimSpace(owner.Name) != ""
		}
	}
	return ownerReference{}, false
}

func selectorMatches(service, workload map[string]string) bool {
	if len(service) == 0 || len(workload) == 0 {
		return false
	}
	for key, value := range service {
		if workload[key] != value {
			return false
		}
	}
	return true
}

func classifyTopologyLayer(namespace, name string) string {
	value := strings.ToLower(namespace + "/" + name)
	switch {
	case containsAny(value, "prometheus", "grafana", "loki", "alloy", "fluent", "clickhouse", "clickvisual", "alertmanager"):
		return "observability"
	case containsAny(value, "mysql", "mariadb", "postgres", "mongo", "redis", "valkey", "rabbitmq", "kafka", "etcd", "consul", "nacos", "activemq"):
		return "data"
	case containsAny(value, "higress", "ingress", "gateway"):
		return "entry"
	default:
		return "application"
	}
}

func topologyLayerOrder(layer string) int {
	switch layer {
	case "entry":
		return 0
	case "application":
		return 1
	case "data":
		return 2
	case "observability":
		return 3
	default:
		return 4
	}
}

func containsAny(value string, values ...string) bool {
	for _, candidate := range values {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func isSystemNamespace(namespace string) bool {
	switch namespace {
	case "kube-system", "kube-public", "kube-node-lease", "default":
		return true
	default:
		return false
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func topologyLabels(source map[string]string) map[string]string {
	allowed := []string{"app", "app.kubernetes.io/name", "app.kubernetes.io/component", "ops-deploy.io/project", "ops-deploy.io/environment"}
	result := make(map[string]string)
	for _, key := range allowed {
		if value := strings.TrimSpace(source[key]); value != "" {
			result[key] = value
		}
	}
	return result
}

func mergeServicePorts(current, incoming []ServicePort) []ServicePort {
	seen := make(map[string]struct{}, len(current))
	for _, port := range current {
		seen[port.Name+":"+strconv.Itoa(port.Port)] = struct{}{}
	}
	for _, port := range incoming {
		key := port.Name + ":" + strconv.Itoa(port.Port)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		current = append(current, port)
	}
	sort.Slice(current, func(i, j int) bool {
		if current[i].Port == current[j].Port {
			return current[i].Name < current[j].Name
		}
		return current[i].Port < current[j].Port
	})
	return current
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func defaultPath(value string) string {
	if strings.TrimSpace(value) == "" {
		return "/"
	}
	return value
}

func selectPrometheusService(services []topologyService) (topologyService, bool) {
	score := func(service topologyService) int {
		name := strings.ToLower(service.name)
		switch {
		case name == "prometheus-operated":
			return 100
		case strings.Contains(name, "kube-prometheus-prometheus"):
			return 90
		case strings.Contains(name, "prometheus") && !strings.Contains(name, "operator"):
			return 50
		default:
			return 0
		}
	}
	best, bestScore := topologyService{}, 0
	for _, service := range services {
		if len(service.ports) == 0 {
			continue
		}
		if current := score(service); current > bestScore {
			best, bestScore = service, current
		}
	}
	return best, bestScore > 0
}

type prometheusSignals struct {
	available   bool
	cpu         map[string]float64
	memory      map[string]float64
	restarts    map[string]float64
	connections []runtimeTopologyConnection
}

func (s *Service) collectPrometheusTopologySignals(ctx context.Context, kubeconfig string, service topologyService, namespaces map[string]struct{}) (prometheusSignals, []ApplicationAlert, []string) {
	signals := prometheusSignals{
		cpu: make(map[string]float64), memory: make(map[string]float64), restarts: make(map[string]float64),
		connections: make([]runtimeTopologyConnection, 0),
	}
	port := service.ports[0].Port
	for _, candidate := range service.ports {
		if candidate.Port == 9090 || strings.Contains(strings.ToLower(candidate.Name), "web") {
			port = candidate.Port
			break
		}
	}
	selector := prometheusNamespaceSelector(namespaces)
	queries := map[string]string{
		"cpu":         fmt.Sprintf(`sum by (namespace,pod) (rate(container_cpu_usage_seconds_total{container!="",image!="",namespace=~%q}[5m]))`, selector),
		"memory":      fmt.Sprintf(`sum by (namespace,pod) (container_memory_working_set_bytes{container!="",image!="",namespace=~%q})`, selector),
		"restarts":    fmt.Sprintf(`sum by (namespace,pod) (increase(kube_pod_container_status_restarts_total{namespace=~%q}[15m]))`, selector),
		"alerts":      `ALERTS{alertstate="firing"}`,
		"tempo_graph": `sum by (client,server) (rate(traces_service_graph_request_total[5m]))`,
		"istio_graph": `sum by (source_workload,source_workload_namespace,destination_workload,destination_workload_namespace,request_protocol) (rate(istio_requests_total{reporter="source"}[5m]))`,
	}
	type queryResult struct {
		name   string
		vector prometheusVector
		err    error
	}
	results := make(chan queryResult, len(queries))
	var group sync.WaitGroup
	for name, query := range queries {
		name, query := name, query
		group.Add(1)
		go func() {
			defer group.Done()
			vector, err := s.queryPrometheus(ctx, kubeconfig, service.namespace, service.name, port, query)
			results <- queryResult{name: name, vector: vector, err: err}
		}()
	}
	group.Wait()
	close(results)
	warnings := make([]string, 0)
	alerts := make([]ApplicationAlert, 0)
	successful := 0
	for result := range results {
		if result.err != nil {
			warnings = append(warnings, "Prometheus "+result.name+" 查询不可用，已使用 Kubernetes 状态继续展示")
			continue
		}
		successful++
		if result.name == "alerts" {
			alerts = decodeApplicationAlerts(result.vector, namespaces)
			continue
		}
		if result.name == "tempo_graph" || result.name == "istio_graph" {
			signals.connections = append(signals.connections, decodeRuntimeTopologyConnections(result.name, result.vector, namespaces)...)
			continue
		}
		target := map[string]float64{}
		switch result.name {
		case "cpu":
			target = signals.cpu
		case "memory":
			target = signals.memory
		case "restarts":
			target = signals.restarts
		}
		for _, item := range result.vector.Data.Result {
			namespace, pod := item.Metric["namespace"], item.Metric["pod"]
			if namespace == "" || pod == "" || len(item.Value) < 2 {
				continue
			}
			value, parseErr := strconv.ParseFloat(fmt.Sprint(item.Value[1]), 64)
			if parseErr == nil {
				target[namespace+"/"+pod] += value
			}
		}
	}
	signals.available = successful > 0
	sort.Strings(warnings)
	return signals, alerts, warnings
}

func decodeRuntimeTopologyConnections(source string, vector prometheusVector, namespaces map[string]struct{}) []runtimeTopologyConnection {
	result := make([]runtimeTopologyConnection, 0)
	for _, item := range vector.Data.Result {
		if len(item.Value) < 2 {
			continue
		}
		rate, err := strconv.ParseFloat(fmt.Sprint(item.Value[1]), 64)
		if err != nil || rate <= 0 {
			continue
		}
		connection := runtimeTopologyConnection{requestRate: rate}
		switch source {
		case "istio_graph":
			connection.sourceName = item.Metric["source_workload"]
			connection.sourceNamespace = item.Metric["source_workload_namespace"]
			connection.targetName = item.Metric["destination_workload"]
			connection.targetNamespace = item.Metric["destination_workload_namespace"]
			connection.protocol = item.Metric["request_protocol"]
			connection.evidence = "Prometheus istio_requests_total（最近 5 分钟）"
		case "tempo_graph":
			connection.sourceName = item.Metric["client"]
			connection.targetName = item.Metric["server"]
			connection.protocol = "trace"
			connection.evidence = "Prometheus Tempo service graph（最近 5 分钟）"
		}
		if connection.sourceName == "" || connection.targetName == "" {
			continue
		}
		if len(namespaces) > 0 {
			if connection.sourceNamespace != "" {
				if _, allowed := namespaces[connection.sourceNamespace]; !allowed {
					continue
				}
			}
			if connection.targetNamespace != "" {
				if _, allowed := namespaces[connection.targetNamespace]; !allowed {
					continue
				}
			}
		}
		result = append(result, connection)
	}
	return result
}

func prometheusNamespaceSelector(namespaces map[string]struct{}) string {
	if len(namespaces) == 0 {
		return ".+"
	}
	values := make([]string, 0, len(namespaces))
	for namespace := range namespaces {
		values = append(values, regexp.QuoteMeta(namespace))
	}
	sort.Strings(values)
	return "^(" + strings.Join(values, "|") + ")$"
}

func (s *Service) queryPrometheus(ctx context.Context, kubeconfig, namespace, service string, port int, query string) (prometheusVector, error) {
	path := fmt.Sprintf(
		"/api/v1/namespaces/%s/services/http:%s:%d/proxy/api/v1/query?query=%s",
		url.PathEscape(namespace), url.PathEscape(service), port, url.QueryEscape(query),
	)
	payload, err := s.capture(ctx, "", s.config.Tools.Kubectl, []string{"get", "--raw", path}, kubeconfig)
	if err != nil {
		return prometheusVector{}, err
	}
	var vector prometheusVector
	if err := json.Unmarshal(payload, &vector); err != nil {
		return prometheusVector{}, err
	}
	if vector.Status != "success" || vector.Data.ResultType != "vector" {
		return prometheusVector{}, errors.New("Prometheus returned an unsuccessful vector response")
	}
	return vector, nil
}

func decodeApplicationAlerts(vector prometheusVector, namespaces map[string]struct{}) []ApplicationAlert {
	result := make([]ApplicationAlert, 0)
	for _, item := range vector.Data.Result {
		namespace := strings.TrimSpace(item.Metric["namespace"])
		if namespace != "" && len(namespaces) > 0 {
			if _, allowed := namespaces[namespace]; !allowed {
				continue
			}
		}
		name := strings.TrimSpace(item.Metric["alertname"])
		if isIgnoredManagedControlPlaneAlert(name) {
			continue
		}
		alert := ApplicationAlert{
			Name: name, Severity: strings.ToLower(strings.TrimSpace(item.Metric["severity"])),
			State: "warning", Namespace: namespace,
			Workload: firstMetricValue(item.Metric, "workload", "deployment", "statefulset", "daemonset"),
			Pod:      item.Metric["pod"], Service: item.Metric["service"],
			Summary: firstMetricValue(item.Metric, "summary"),
		}
		if alert.Name == "" {
			alert.Name = "Prometheus 告警"
		}
		if alert.Severity == "critical" || alert.Severity == "error" {
			alert.State = "abnormal"
		}
		result = append(result, alert)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].State != result[j].State {
			return stateRank(result[i].State) > stateRank(result[j].State)
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func firstMetricValue(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func isIgnoredManagedControlPlaneAlert(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kubecontrollermanagerdown", "kubeschedulerdown", "kubeetcdunhealthy", "kubeetcdinsufficientmembers", "kubeetcdnoleader":
		return true
	default:
		return false
	}
}

func applyPrometheusMetrics(topology *ApplicationTopology, podOwners map[string]string, signals prometheusSignals) {
	index := make(map[string]int, len(topology.Nodes))
	for position, node := range topology.Nodes {
		index[node.ID] = position
	}
	for pod, workload := range podOwners {
		position, found := index[workload]
		if !found {
			continue
		}
		topology.Nodes[position].CPUCores += signals.cpu[pod]
		topology.Nodes[position].MemoryBytes += signals.memory[pod]
		if restarts := int(signals.restarts[pod] + 0.5); restarts > 0 {
			if restarts > topology.Nodes[position].Restarts {
				topology.Nodes[position].Restarts = restarts
			}
			topology.Nodes[position].State = worseState(topology.Nodes[position].State, "warning")
			topology.Nodes[position].StateReason = fmt.Sprintf("最近 15 分钟发生 %d 次容器重启", restarts)
		}
	}
}

func applyPrometheusAlerts(topology *ApplicationTopology, alerts []ApplicationAlert) {
	for index := range topology.Nodes {
		node := &topology.Nodes[index]
		for _, alert := range alerts {
			if alert.Namespace != "" && alert.Namespace != node.Namespace {
				continue
			}
			matched := (alert.Workload != "" && alert.Workload == node.Name) ||
				(alert.Service != "" && stringSliceContains(node.Services, alert.Service)) ||
				(alert.Pod != "" && strings.HasPrefix(alert.Pod, node.Name+"-"))
			if !matched {
				continue
			}
			node.State = worseState(node.State, alert.State)
			node.StateReason = "活动告警：" + alert.Name
		}
	}
}

func decodeTopologyEndpointRelations(payload []byte, podOwners map[string]string) (map[string]topologyEndpointRelation, error) {
	var response struct {
		Items []struct {
			Kind     string `json:"kind"`
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
				TargetRef struct {
					Kind      string `json:"kind"`
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
				} `json:"targetRef"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	result := make(map[string]topologyEndpointRelation)
	for _, item := range response.Items {
		if item.Kind != "EndpointSlice" {
			continue
		}
		namespace := strings.TrimSpace(item.Metadata.Namespace)
		serviceName := strings.TrimSpace(item.Metadata.Labels["kubernetes.io/service-name"])
		if namespace == "" || serviceName == "" {
			continue
		}
		key := namespace + "/" + serviceName
		relation := result[key]
		if relation.workloads == nil {
			relation.workloads = make(map[string]kubernetesEndpointHealth)
		}
		for _, endpoint := range item.Endpoints {
			if len(endpoint.Addresses) == 0 {
				continue
			}
			relation.total++
			ready := endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready
			terminating := endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating
			if ready && !terminating {
				relation.ready++
			}
			if !strings.EqualFold(endpoint.TargetRef.Kind, "Pod") || strings.TrimSpace(endpoint.TargetRef.Name) == "" {
				continue
			}
			targetNamespace := strings.TrimSpace(endpoint.TargetRef.Namespace)
			if targetNamespace == "" {
				targetNamespace = namespace
			}
			workloadID, found := podOwners[targetNamespace+"/"+strings.TrimSpace(endpoint.TargetRef.Name)]
			if !found {
				continue
			}
			health := relation.workloads[workloadID]
			health.Total++
			if ready && !terminating {
				health.Ready++
			}
			relation.workloads[workloadID] = health
		}
		result[key] = relation
	}
	return result, nil
}

func applyTopologyEndpointRelations(topology *ApplicationTopology, services []topologyService, relations map[string]topologyEndpointRelation) {
	nodeIndex := make(map[string]int, len(topology.Nodes))
	for index, node := range topology.Nodes {
		nodeIndex[node.ID] = index
	}
	serviceKeys := make(map[string]struct{}, len(services))
	for _, service := range services {
		serviceKeys[service.namespace+"/"+service.name] = struct{}{}
	}
	actualTargets := make(map[string]map[string]struct{})
	for serviceKey, relation := range relations {
		if _, known := serviceKeys[serviceKey]; !known {
			continue
		}
		parts := strings.SplitN(serviceKey, "/", 2)
		if len(parts) != 2 {
			continue
		}
		serviceNodeID := "service:" + parts[0] + ":" + parts[1]
		if position, found := nodeIndex[serviceNodeID]; found {
			node := &topology.Nodes[position]
			node.ReadyEndpoints = relation.ready
			node.TotalEndpoints = relation.total
			switch {
			case relation.total == 0:
				node.State = worseState(node.State, "abnormal")
				node.StateReason = "EndpointSlice 没有可用后端"
			case relation.ready == 0:
				node.State = worseState(node.State, "abnormal")
				node.StateReason = fmt.Sprintf("实际端点 0/%d 就绪", relation.total)
			case relation.ready < relation.total:
				node.State = worseState(node.State, "warning")
				node.StateReason = fmt.Sprintf("实际端点 %d/%d 就绪", relation.ready, relation.total)
			default:
				node.StateReason = fmt.Sprintf("EndpointSlice 实际端点 %d/%d 就绪", relation.ready, relation.total)
			}
		}
		actualTargets[serviceNodeID] = make(map[string]struct{}, len(relation.workloads))
		for workloadID, health := range relation.workloads {
			actualTargets[serviceNodeID][workloadID] = struct{}{}
			state := "normal"
			if health.Ready == 0 {
				state = "abnormal"
			} else if health.Ready < health.Total {
				state = "warning"
			}
			topology.Edges = append(topology.Edges, ApplicationEdge{
				ID:     serviceNodeID + "->" + workloadID + ":endpoint",
				Source: serviceNodeID, Target: workloadID, Relation: "endpoint",
				Label:    fmt.Sprintf("%d/%d 端点", health.Ready, health.Total),
				Evidence: "Kubernetes EndpointSlice targetRef", Verified: true, State: state,
				ReadyEndpoints: health.Ready, TotalEndpoints: health.Total,
			})
		}
	}

	filtered := topology.Edges[:0]
	for _, edge := range topology.Edges {
		if edge.Relation != "service_selector" {
			filtered = append(filtered, edge)
			continue
		}
		if targets, found := actualTargets[edge.Source]; found {
			if _, verified := targets[edge.Target]; verified {
				continue
			}
		}
		filtered = append(filtered, edge)
	}
	topology.Edges = filtered
}

func applyRuntimeTopologyConnections(topology *ApplicationTopology, connections []runtimeTopologyConnection) int {
	if len(connections) == 0 {
		return 0
	}
	resolve := func(name, namespace string) string {
		name = strings.TrimSpace(name)
		namespace = strings.TrimSpace(namespace)
		if name == "" {
			return ""
		}
		for _, node := range topology.Nodes {
			if node.Kind == "Ingress" || node.Kind == "Service" {
				continue
			}
			if namespace != "" && node.Namespace != namespace {
				continue
			}
			if node.Name == name || node.Labels["app"] == name ||
				node.Labels["app.kubernetes.io/name"] == name ||
				node.Labels["app.kubernetes.io/component"] == name ||
				stringSliceContains(node.Services, name) {
				return node.ID
			}
		}
		return ""
	}
	type edgeValue struct {
		connection runtimeTopologyConnection
		sourceID   string
		targetID   string
	}
	merged := make(map[string]edgeValue)
	for _, connection := range connections {
		sourceID := resolve(connection.sourceName, connection.sourceNamespace)
		targetID := resolve(connection.targetName, connection.targetNamespace)
		if sourceID == "" || targetID == "" || sourceID == targetID || connection.requestRate <= 0 {
			continue
		}
		key := sourceID + "->" + targetID
		value := merged[key]
		if value.sourceID == "" {
			value = edgeValue{connection: connection, sourceID: sourceID, targetID: targetID}
		} else {
			value.connection.requestRate += connection.requestRate
			if !strings.Contains(value.connection.evidence, connection.evidence) {
				value.connection.evidence += " + " + connection.evidence
			}
		}
		merged[key] = value
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := merged[key]
		topology.Edges = append(topology.Edges, ApplicationEdge{
			ID: "runtime:" + key, Source: value.sourceID, Target: value.targetID,
			Relation: "runtime_request", Protocol: strings.ToUpper(value.connection.protocol),
			Label:    fmt.Sprintf("%.2f req/s", value.connection.requestRate),
			Evidence: value.connection.evidence, Verified: true, State: "normal",
			RequestRate: value.connection.requestRate,
		})
	}
	return len(keys)
}

func applyTopologyEdgeStates(topology *ApplicationTopology) {
	index := make(map[string]int, len(topology.Nodes))
	for position, node := range topology.Nodes {
		index[node.ID] = position
	}
	for _, edge := range topology.Edges {
		if edge.Relation != "ingress_route" {
			continue
		}
		source, sourceFound := index[edge.Source]
		target, targetFound := index[edge.Target]
		if !sourceFound || !targetFound || topology.Nodes[target].State == "normal" {
			continue
		}
		topology.Nodes[source].State = worseState(topology.Nodes[source].State, topology.Nodes[target].State)
		topology.Nodes[source].StateReason = "后端 " + topology.Nodes[target].Name + "：" + topology.Nodes[target].StateReason
	}
	states := make(map[string]string, len(topology.Nodes))
	for _, node := range topology.Nodes {
		states[node.ID] = node.State
	}
	for index := range topology.Edges {
		edge := &topology.Edges[index]
		if edge.State == "" {
			edge.State = "normal"
		}
		if targetState := states[edge.Target]; targetState != "" {
			edge.State = worseState(edge.State, targetState)
		}
	}
}

func stringSliceContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func worseState(current, candidate string) string {
	if stateRank(candidate) > stateRank(current) {
		return candidate
	}
	return current
}

func stateRank(state string) int {
	switch state {
	case "abnormal":
		return 3
	case "warning":
		return 2
	default:
		return 1
	}
}

func summarizeTopology(topology *ApplicationTopology) {
	topology.Summary = TopologySummary{Total: len(topology.Nodes), Connections: len(topology.Edges)}
	for _, node := range topology.Nodes {
		switch node.State {
		case "abnormal":
			topology.Summary.Abnormal++
		case "warning":
			topology.Summary.Warning++
		default:
			topology.Summary.Normal++
		}
	}
	for _, edge := range topology.Edges {
		switch edge.Relation {
		case "runtime_request":
			topology.Summary.RuntimeConnections++
		case "endpoint":
			topology.Summary.EndpointConnections++
		default:
			topology.Summary.DeclaredConnections++
		}
	}
}
