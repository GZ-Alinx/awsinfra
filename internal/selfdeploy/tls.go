package selfdeploy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

type kubernetesSecret struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

func (d *Deployer) ingressTLSSecretDocument(ctx context.Context) ([]byte, error) {
	ingress := d.config.Kubernetes.Ingress
	payload, err := d.kubectl(ctx, nil, false,
		"get", "secret", ingress.TLSSecretName,
		"--namespace", ingress.TLSSecretSourceNamespace,
		"--output", "json",
	)
	if err != nil {
		return nil, fmt.Errorf("read Higress TLS Secret %s/%s: %w", ingress.TLSSecretSourceNamespace, ingress.TLSSecretName, err)
	}
	defer clear(payload)
	return buildTLSSecretDocument(payload, d.config.Kubernetes.Namespace, ingress.TLSSecretName, ingress.Host, time.Now())
}

func buildTLSSecretDocument(source []byte, namespace, name, host string, now time.Time) ([]byte, error) {
	var secret kubernetesSecret
	if err := json.Unmarshal(source, &secret); err != nil {
		return nil, fmt.Errorf("parse source TLS Secret: %w", err)
	}
	if secret.Type != "kubernetes.io/tls" {
		return nil, fmt.Errorf("source Secret type is %q, expected kubernetes.io/tls", secret.Type)
	}
	certificatePEM, err := base64.StdEncoding.Strict().DecodeString(secret.Data["tls.crt"])
	if err != nil || len(certificatePEM) == 0 {
		return nil, errors.New("source TLS Secret contains an invalid tls.crt")
	}
	defer clear(certificatePEM)
	privateKey, err := base64.StdEncoding.Strict().DecodeString(secret.Data["tls.key"])
	if err != nil || len(privateKey) == 0 {
		return nil, errors.New("source TLS Secret contains an invalid tls.key")
	}
	defer clear(privateKey)
	if _, err := tls.X509KeyPair(certificatePEM, privateKey); err != nil {
		return nil, fmt.Errorf("source TLS certificate and private key are invalid or do not match: %w", err)
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("source TLS Secret tls.crt is not an X.509 certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse source TLS certificate: %w", err)
	}
	if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return nil, fmt.Errorf("source TLS certificate is not valid at %s", now.UTC().Format(time.RFC3339))
	}
	if err := certificate.VerifyHostname(strings.TrimSpace(host)); err != nil {
		return nil, fmt.Errorf("source TLS certificate does not cover %s: %w", host, err)
	}
	document := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "ops-deploy-platform",
			},
		},
		"type": "kubernetes.io/tls",
		"data": map[string]string{
			"tls.crt": secret.Data["tls.crt"],
			"tls.key": secret.Data["tls.key"],
		},
	}
	return json.Marshal(document)
}
