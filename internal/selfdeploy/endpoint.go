package selfdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var fakeIPNetwork = mustCIDR("198.18.0.0/15")

type dohResponse struct {
	Status int `json:"Status"`
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

func mustCIDR(value string) *net.IPNet {
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		panic(err)
	}
	return network
}

func (d *Deployer) stabilizeKubeconfigEndpoint(ctx context.Context, endpoint string) error {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return errors.New("AWS returned an invalid EKS API endpoint")
	}
	host := parsed.Hostname()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if !endpointRequiresPublicResolution(addresses, err) {
		return nil
	}

	address, err := resolvePublicIPv4(ctx, host)
	if err != nil {
		return fmt.Errorf("本机 DNS 无法可靠解析 EKS API，且无法通过加密公共 DNS 获取真实地址: %w", err)
	}
	if err := patchKubeconfigEndpoint(d.config.Cluster.Kubeconfig, d.config.Cluster.ContextAlias, host, address.String()); err != nil {
		return err
	}
	if len(addresses) == 0 {
		fmt.Fprintf(d.out, "检测到本机 DNS 无法解析 EKS API：独立 kubeconfig 已通过加密公共 DNS 固定真实地址 %s，并继续使用 %s 做 TLS 主机校验\n", address, host)
	} else {
		fmt.Fprintf(d.out, "检测到本机 Fake-IP DNS：独立 kubeconfig 已固定 EKS 真实地址 %s，并继续使用 %s 做 TLS 主机校验\n", address, host)
	}
	return nil
}

func endpointRequiresPublicResolution(addresses []net.IPAddr, lookupErr error) bool {
	if lookupErr != nil || len(addresses) == 0 {
		return true
	}
	for _, address := range addresses {
		if !fakeIPNetwork.Contains(address.IP) {
			return false
		}
	}
	return true
}

func resolvePublicIPv4(ctx context.Context, host string) (net.IP, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	providers := []string{
		"https://cloudflare-dns.com/dns-query",
		"https://dns.google/resolve",
	}
	var lastErr error
	for _, provider := range providers {
		requestURL, _ := url.Parse(provider)
		query := requestURL.Query()
		query.Set("name", host)
		query.Set("type", "A")
		requestURL.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
		if err != nil {
			lastErr = err
			continue
		}
		request.Header.Set("Accept", "application/dns-json")
		response, err := client.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		var result dohResponse
		decodeErr := json.NewDecoder(response.Body).Decode(&result)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil || result.Status != 0 {
			lastErr = fmt.Errorf("encrypted DNS provider returned HTTP %d, DNS status %d", response.StatusCode, result.Status)
			continue
		}
		for _, answer := range result.Answer {
			address := net.ParseIP(strings.TrimSpace(answer.Data))
			if answer.Type == 1 && address != nil && address.To4() != nil && address.IsGlobalUnicast() && !fakeIPNetwork.Contains(address) {
				return address.To4(), nil
			}
		}
		lastErr = errors.New("encrypted DNS response did not contain a public IPv4 address")
	}
	if lastErr == nil {
		lastErr = errors.New("no encrypted DNS provider is configured")
	}
	return nil, lastErr
}

func patchKubeconfigEndpoint(path, contextAlias, tlsServerName, address string) error {
	payload, err := os.ReadFile(path) // #nosec G304 -- path is the dedicated operator-selected kubeconfig.
	if err != nil {
		return fmt.Errorf("read isolated kubeconfig: %w", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(payload, &document); err != nil {
		return fmt.Errorf("parse isolated kubeconfig: %w", err)
	}
	clusterName := ""
	contexts, _ := document["contexts"].([]any)
	for _, raw := range contexts {
		item, _ := raw.(map[string]any)
		if stringValueAny(item["name"]) != contextAlias {
			continue
		}
		contextValue, _ := item["context"].(map[string]any)
		clusterName = stringValueAny(contextValue["cluster"])
		break
	}
	if clusterName == "" {
		return fmt.Errorf("isolated kubeconfig does not contain context %q", contextAlias)
	}
	patched := false
	clusters, _ := document["clusters"].([]any)
	for _, raw := range clusters {
		item, _ := raw.(map[string]any)
		if stringValueAny(item["name"]) != clusterName {
			continue
		}
		cluster, _ := item["cluster"].(map[string]any)
		if cluster == nil {
			return errors.New("isolated kubeconfig cluster entry is invalid")
		}
		cluster["server"] = "https://" + address
		cluster["tls-server-name"] = tlsServerName
		patched = true
		break
	}
	if !patched {
		return fmt.Errorf("isolated kubeconfig does not contain cluster %q", clusterName)
	}
	updated, err := yaml.Marshal(document)
	if err != nil {
		return fmt.Errorf("render isolated kubeconfig: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".kubeconfig-*")
	if err != nil {
		return fmt.Errorf("create isolated kubeconfig update: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(updated); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace isolated kubeconfig: %w", err)
	}
	return nil
}

func stringValueAny(value any) string {
	result, _ := value.(string)
	return strings.TrimSpace(result)
}
