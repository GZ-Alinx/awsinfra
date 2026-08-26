package selfdeploy

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/go-sql-driver/mysql"
)

var requiredSecretKeys = []string{
	"OPS_DEPLOY_PASSWORD_HASH",
	"OPS_DEPLOY_CREDENTIAL_KEY",
	"OPS_MYSQL_PASSWORD",
	"OPS_MYSQL_ROOT_PASSWORD",
	"OPS_REDIS_PASSWORD",
}

func loadSecrets(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes secrets file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("Kubernetes secrets file must use mode 0600")
	}
	file, err := os.Open(path) // #nosec G304 -- selected by the local operator.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid secrets assignment at line %d", line)
		}
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for _, key := range requiredSecretKeys {
		value := strings.TrimSpace(values[key])
		if value == "" || strings.Contains(value, "replace-with") || strings.Contains(value, "replace-salt") {
			return nil, fmt.Errorf("%s is missing or still contains the example value", key)
		}
	}
	if !strings.HasPrefix(values["OPS_DEPLOY_PASSWORD_HASH"], "$argon2id$") {
		return nil, errors.New("OPS_DEPLOY_PASSWORD_HASH must be an Argon2id hash")
	}
	key, err := base64.StdEncoding.Strict().DecodeString(values["OPS_DEPLOY_CREDENTIAL_KEY"])
	if err != nil || len(key) != 32 {
		return nil, errors.New("OPS_DEPLOY_CREDENTIAL_KEY must be a base64-encoded 32-byte key")
	}
	clear(key)
	for _, key := range []string{"OPS_MYSQL_PASSWORD", "OPS_MYSQL_ROOT_PASSWORD", "OPS_REDIS_PASSWORD"} {
		if len(values[key]) < 16 {
			return nil, fmt.Errorf("%s must contain at least 16 characters", key)
		}
	}
	dsn := mysql.NewConfig()
	dsn.User = "ops"
	dsn.Passwd = values["OPS_MYSQL_PASSWORD"]
	dsn.Net = "tcp"
	dsn.Addr = "ops-deploy-mysql:3306"
	dsn.DBName = "ops_deploy"
	dsn.ParseTime = true
	dsn.Collation = "utf8mb4_unicode_ci"
	dsn.Params = map[string]string{"charset": "utf8mb4", "loc": "UTC"}
	values["OPS_MYSQL_DSN"] = dsn.FormatDSN()
	return values, nil
}

func secretDocument(namespace string, values map[string]string) ([]byte, error) {
	data := make(map[string]string, len(values))
	for key, value := range values {
		data[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	document := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]string{
			"name":      "ops-deploy-secrets",
			"namespace": namespace,
		},
		"type": "Opaque",
		"data": data,
	}
	return json.Marshal(document)
}
