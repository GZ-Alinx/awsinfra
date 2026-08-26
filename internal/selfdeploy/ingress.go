package selfdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

const managedIngressName = "ops-deploy-platform"

type ingressList struct {
	Items []struct {
		Metadata struct {
			Name        string            `json:"name"`
			Namespace   string            `json:"namespace"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Rules []struct {
				Host string `json:"host"`
			} `json:"rules"`
		} `json:"spec"`
	} `json:"items"`
}

type gatewayService struct {
	Spec struct {
		Type  string `json:"type"`
		Ports []struct {
			Port int `json:"port"`
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
}

func (d *Deployer) validateIngressHostOwnership(ctx context.Context) error {
	host := d.config.Kubernetes.Ingress.Host
	payload, err := d.kubectl(ctx, nil, false, "get", "ingresses.networking.k8s.io", "--all-namespaces", "--output", "json")
	if err != nil {
		return fmt.Errorf("检查 Ingress 域名占用失败；当前 Kubernetes 身份必须能列出所有 Namespace 的 Ingress: %w", err)
	}
	conflicts, err := findIngressHostConflicts(payload, host, d.config.Kubernetes.Namespace, managedIngressName)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("域名 %s 已被其他 Ingress 占用：%s；请先确认并移除重复路由，发布工具不会覆盖其他项目的网关配置", host, strings.Join(conflicts, "、"))
	}
	destination := fmt.Sprintf("ops-deploy-platform.%s.svc.cluster.local:80", d.config.Kubernetes.Namespace)
	aliases, err := findIngressDestinationAliases(payload, host, destination, d.config.Kubernetes.Namespace, managedIngressName)
	if err != nil {
		return err
	}
	if len(aliases) > 0 {
		fmt.Fprintf(d.errOut, "警告：检测到仍指向本平台的其他 Higress 域名：%s。发布工具不会自动删除这些手工路由；确认新域名可用后应清理，避免额外暴露入口。\n", strings.Join(aliases, "、"))
	}
	return nil
}

func findIngressHostConflicts(payload []byte, host, managedNamespace, managedName string) ([]string, error) {
	var list ingressList
	if err := json.Unmarshal(payload, &list); err != nil {
		return nil, fmt.Errorf("解析 Ingress 列表失败: %w", err)
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	var conflicts []string
	for _, item := range list.Items {
		if item.Metadata.Namespace == managedNamespace && item.Metadata.Name == managedName {
			continue
		}
		for _, rule := range item.Spec.Rules {
			candidate := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rule.Host)), ".")
			if candidate != host {
				continue
			}
			name := item.Metadata.Namespace + "/" + item.Metadata.Name
			if destination := strings.TrimSpace(item.Metadata.Annotations["higress.io/destination"]); destination != "" {
				name += "（目标 " + destination + "）"
			}
			conflicts = append(conflicts, name)
			break
		}
	}
	return conflicts, nil
}

func findIngressDestinationAliases(payload []byte, desiredHost, destination, managedNamespace, managedName string) ([]string, error) {
	var list ingressList
	if err := json.Unmarshal(payload, &list); err != nil {
		return nil, fmt.Errorf("解析 Ingress 列表失败: %w", err)
	}
	desiredHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(desiredHost)), ".")
	var aliases []string
	for _, item := range list.Items {
		if item.Metadata.Namespace == managedNamespace && item.Metadata.Name == managedName {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(item.Metadata.Annotations["higress.io/destination"]), destination) {
			continue
		}
		for _, rule := range item.Spec.Rules {
			host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rule.Host)), ".")
			if host == "" || host == desiredHost {
				continue
			}
			aliases = append(aliases, fmt.Sprintf("%s/%s（%s）", item.Metadata.Namespace, item.Metadata.Name, host))
		}
	}
	return aliases, nil
}

func (d *Deployer) gatewayAddress(ctx context.Context) (string, error) {
	ingress := d.config.Kubernetes.Ingress
	payload, err := d.kubectl(ctx, nil, false, "get", "service", ingress.GatewayServiceName,
		"--namespace", ingress.GatewayServiceNamespace, "--output", "json")
	if err != nil {
		return "", fmt.Errorf("读取网关 Service %s/%s 失败: %w", ingress.GatewayServiceNamespace, ingress.GatewayServiceName, err)
	}
	var service gatewayService
	if err := json.Unmarshal(payload, &service); err != nil {
		return "", fmt.Errorf("解析网关 Service 失败: %w", err)
	}
	if service.Spec.Type != "LoadBalancer" {
		return "", fmt.Errorf("网关 Service %s/%s 类型是 %s，不是 LoadBalancer", ingress.GatewayServiceNamespace, ingress.GatewayServiceName, service.Spec.Type)
	}
	hasHTTPS := false
	for _, port := range service.Spec.Ports {
		if port.Port == 443 {
			hasHTTPS = true
			break
		}
	}
	if !hasHTTPS {
		return "", fmt.Errorf("网关 Service %s/%s 未开放 443 端口", ingress.GatewayServiceNamespace, ingress.GatewayServiceName)
	}
	for _, endpoint := range service.Status.LoadBalancer.Ingress {
		if value := strings.TrimSpace(endpoint.IP); value != "" {
			return value, nil
		}
		if value := strings.TrimSuffix(strings.TrimSpace(endpoint.Hostname), "."); value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("网关 Service %s/%s 尚未获得外部地址", ingress.GatewayServiceNamespace, ingress.GatewayServiceName)
}

func (d *Deployer) syncManagedIngressAddress(ctx context.Context) error {
	address, err := d.gatewayAddress(ctx)
	if err != nil {
		return fmt.Errorf("同步平台 Ingress 负载均衡地址失败: %w", err)
	}
	entry := map[string]string{"hostname": address}
	if net.ParseIP(address) != nil {
		entry = map[string]string{"ip": address}
	}
	patch, err := json.Marshal(map[string]any{
		"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{entry}}},
	})
	if err != nil {
		return err
	}
	if _, err := d.kubectl(ctx, nil, true,
		"patch", "ingress", managedIngressName,
		"--namespace", d.config.Kubernetes.Namespace,
		"--subresource=status", "--type=merge", "--patch", string(patch)); err != nil {
		return fmt.Errorf("回写平台 Ingress status 失败: %w", err)
	}
	fmt.Fprintf(d.out, "Ingress ADDRESS 已同步：%s\n", address)
	return nil
}

func resolveEndpointIPv4(ctx context.Context, endpoint string) (net.IP, error) {
	if address := net.ParseIP(strings.TrimSpace(endpoint)); address != nil {
		if address.To4() == nil || !address.IsGlobalUnicast() || fakeIPNetwork.Contains(address) {
			return nil, errors.New("网关返回的不是可用公网 IPv4 地址")
		}
		return address.To4(), nil
	}
	return resolvePublicIPv4(ctx, endpoint)
}
