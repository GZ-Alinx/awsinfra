package selfdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

type platformHealth struct {
	Status string `json:"status"`
}

func (d *Deployer) verifyPlatformHealth(ctx context.Context) error {
	proxyPath := fmt.Sprintf("/api/v1/namespaces/%s/services/http:ops-deploy-platform:80/proxy/api/health", url.PathEscape(d.config.Kubernetes.Namespace))
	payload, err := d.kubectl(ctx, nil, false, "get", "--raw="+proxyPath)
	if err != nil {
		return fmt.Errorf("平台集群内健康检查失败；Service、Pod 或 /api/health 不可用: %w", err)
	}
	if err := validateHealthPayload(payload); err != nil {
		return fmt.Errorf("平台集群内健康检查异常: %w", err)
	}
	fmt.Fprintln(d.out, "集群内健康检查通过：平台、MySQL、Redis 均可访问")

	if !d.config.Kubernetes.Ingress.Enabled {
		return nil
	}
	gateway, err := d.gatewayAddress(ctx)
	if err != nil {
		return err
	}
	gatewayIP, err := resolveEndpointIPv4(ctx, gateway)
	if err != nil {
		return fmt.Errorf("解析网关外部地址 %s 失败: %w", gateway, err)
	}
	host := d.config.Kubernetes.Ingress.Host
	if err := retryHealthCheck(ctx, 6, time.Second, func() error {
		return requestPlatformHealth(ctx, host, gatewayIP)
	}); err != nil {
		return fmt.Errorf("Higress 直连检查失败（域名=%s，网关=%s/%s，地址=%s）：%w", host,
			d.config.Kubernetes.Ingress.GatewayServiceNamespace, d.config.Kubernetes.Ingress.GatewayServiceName, gatewayIP, err)
	}
	fmt.Fprintf(d.out, "Higress 路由与 TLS 检查通过：%s -> %s\n", host, gatewayIP)

	publicIP, err := resolvePublicIPv4(ctx, host)
	if err != nil {
		fmt.Fprintf(d.errOut, "警告：公网 DNS 尚未解析 %s（%v）。集群与 Higress 已正常，但浏览器暂时无法通过该域名访问；请在权威 DNS 添加指向 Higress LoadBalancer 的 CNAME/A 记录。\n", host, err)
		return nil
	}
	publicErr := retryHealthCheck(ctx, 3, time.Second, func() error {
		return requestPlatformHealthPublic(ctx, host)
	})
	if publicErr != nil {
		curlErr := d.requestPlatformHealthWithCurl(ctx, host)
		if curlErr != nil {
			fmt.Fprintf(d.errOut, "警告：公网入口检查未通过（%s -> %s）。集群内服务和 Higress/TLS 已通过，因此本次发布保留成功；请单独检查 Cloudflare、代理或公网链路。Go=%v；curl=%v\n", host, publicIP, publicErr, curlErr)
			return nil
		}
		fmt.Fprintf(d.out, "公网 HTTPS 健康检查通过（系统 curl 兼容路径）：https://%s/api/health（%s）\n", host, publicIP)
		return nil
	}
	fmt.Fprintf(d.out, "公网 HTTPS 健康检查通过：https://%s/api/health（%s）\n", host, publicIP)
	return nil
}

func (d *Deployer) requestPlatformHealthWithCurl(ctx context.Context, host string) error {
	if _, err := exec.LookPath("curl"); err != nil {
		return errors.New("本机未安装 curl，无法执行兼容检查")
	}
	payload, err := d.run(ctx, nil, false, "curl",
		"--silent", "--show-error", "--fail", "--max-time", "20",
		"--header", "Accept: application/json",
		"https://"+strings.TrimSpace(host)+"/api/health",
	)
	if err != nil {
		return err
	}
	return validateHealthPayload(payload)
}

func retryHealthCheck(ctx context.Context, attempts int, initialDelay time.Duration, check func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	delay := initialDelay
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := check(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == attempts {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("健康检查被取消: %w", ctx.Err())
		case <-timer.C:
		}
		if delay > 0 && delay < 8*time.Second {
			delay *= 2
		}
	}
	return lastErr
}

func validateHealthPayload(payload []byte) error {
	var health platformHealth
	if err := json.Unmarshal(payload, &health); err != nil {
		return fmt.Errorf("响应不是合法 JSON: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(health.Status), "ok") {
		return fmt.Errorf("status=%q，预期为 ok", health.Status)
	}
	return nil
}

func requestPlatformHealth(ctx context.Context, host string, address net.IP) error {
	if address == nil || address.To4() == nil {
		return errors.New("没有可用的 IPv4 地址")
	}
	host = strings.TrimSpace(host)
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		DialContext: func(dialContext context.Context, network, target string) (net.Conn, error) {
			targetHost, targetPort, err := net.SplitHostPort(target)
			if err != nil {
				return nil, err
			}
			if strings.EqualFold(strings.TrimSuffix(targetHost, "."), strings.TrimSuffix(host, ".")) {
				target = net.JoinHostPort(address.String(), targetPort)
			}
			return dialer.DialContext(dialContext, network, target)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	return performPlatformHealthRequest(ctx, client, host)
}

func requestPlatformHealthPublic(ctx context.Context, host string) error {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return errors.New("系统 HTTP Transport 类型不受支持")
	}
	cloned := transport.Clone()
	defer cloned.CloseIdleConnections()
	client := &http.Client{Transport: cloned, Timeout: 20 * time.Second}
	return performPlatformHealthRequest(ctx, client, host)
}

func performPlatformHealthRequest(ctx context.Context, client *http.Client, host string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/api/health", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ops-deploy-platform-release-check/1.0")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		excerpt := strings.TrimSpace(string(payload))
		if len(excerpt) > 300 {
			excerpt = excerpt[:300]
		}
		return fmt.Errorf("HTTP %d，响应=%q", response.StatusCode, excerpt)
	}
	return validateHealthPayload(payload)
}
