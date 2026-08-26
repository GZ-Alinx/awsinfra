package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/GZ-Alinx/awsinfra/internal/environment"
	statusservice "github.com/GZ-Alinx/awsinfra/internal/status"
)

func main() {
	var (
		mysqlAddress = flag.String("mysql-address", "127.0.0.1:3306", "MySQL TCP address")
		mysqlUser    = flag.String("mysql-user", "ops", "MySQL user")
		mysqlDB      = flag.String("mysql-database", "ops_deploy", "MySQL database")
		target       = flag.String("environment", "", "platform environment target name")
		kubeconfig   = flag.String("kubeconfig", "", "optional kubeconfig path")
		contextName  = flag.String("context", "", "optional kubeconfig context")
		backupPath   = flag.String("backup", "", "required path for the original JSON backup")
		kubectl      = flag.String("kubectl", "kubectl", "kubectl executable")
		policy       = flag.String("policy", "more-complete", "sync policy: more-complete or cluster")
	)
	flag.Parse()
	if *target == "" || *backupPath == "" {
		exitError(errors.New("--environment and --backup are required"))
	}
	if *policy != "more-complete" && *policy != "cluster" {
		exitError(errors.New("--policy must be more-complete or cluster"))
	}
	password := os.Getenv("OPS_SYNC_MYSQL_PASSWORD")
	if password == "" {
		exitError(errors.New("OPS_SYNC_MYSQL_PASSWORD is required"))
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=true&loc=UTC&timeout=10s&readTimeout=15s&writeTimeout=15s",
		*mysqlUser, password, *mysqlAddress, *mysqlDB)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		exitError(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		exitError(fmt.Errorf("connect MySQL: %w", err))
	}

	original, err := loadConfig(ctx, db, *target, false)
	if err != nil {
		exitError(err)
	}
	if err := writeBackup(*backupPath, original); err != nil {
		exitError(err)
	}
	var doc environment.Document
	if err := json.Unmarshal(original, &doc); err != nil {
		exitError(fmt.Errorf("decode environment config: %w", err))
	}
	payload, err := readIngresses(ctx, *kubectl, *kubeconfig, *contextName)
	if err != nil {
		exitError(err)
	}
	items, err := statusservice.DecodeKubernetesIngressList(payload)
	if err != nil {
		exitError(fmt.Errorf("decode Kubernetes Ingress inventory: %w", err))
	}
	var report statusservice.IngressConfigSyncReport
	if *policy == "cluster" {
		report = statusservice.SyncIngressesToDomainConfigFromCluster(doc, items)
	} else {
		report = statusservice.SyncIngressesToDomainConfig(doc, items)
	}
	if report.UpdatedDomains == 0 {
		printResult(*target, *backupPath, report, false)
		return
	}
	if err := environment.Validate(doc); err != nil {
		exitError(fmt.Errorf("refuse to save invalid synchronized config: %w", err))
	}
	updated, err := json.Marshal(doc)
	if err != nil {
		exitError(fmt.Errorf("encode synchronized config: %w", err))
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		exitError(err)
	}
	locked, err := loadConfig(ctx, tx, *target, true)
	if err != nil {
		_ = tx.Rollback()
		exitError(err)
	}
	if !bytes.Equal(bytes.TrimSpace(locked), bytes.TrimSpace(original)) {
		_ = tx.Rollback()
		exitError(errors.New("environment config changed after backup; synchronization aborted without writing"))
	}
	result, err := tx.ExecContext(ctx, "UPDATE environments SET config_json = ? WHERE name = ?", updated, *target)
	if err != nil {
		_ = tx.Rollback()
		exitError(fmt.Errorf("update environment config: %w", err))
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		_ = tx.Rollback()
		exitError(fmt.Errorf("unexpected updated row count: %d", affected))
	}
	if err := tx.Commit(); err != nil {
		exitError(err)
	}
	printResult(*target, *backupPath, report, true)
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadConfig(ctx context.Context, queryer rowQueryer, target string, lock bool) ([]byte, error) {
	query := "SELECT config_json FROM environments WHERE name = ?"
	if lock {
		query += " FOR UPDATE"
	}
	var payload []byte
	if err := queryer.QueryRowContext(ctx, query, target).Scan(&payload); err != nil {
		return nil, fmt.Errorf("load environment %s: %w", target, err)
	}
	return payload, nil
}

func readIngresses(ctx context.Context, kubectl, kubeconfig, contextName string) ([]byte, error) {
	args := make([]string, 0, 10)
	if kubeconfig != "" {
		args = append(args, "--kubeconfig", kubeconfig)
	}
	if contextName != "" {
		args = append(args, "--context", contextName)
	}
	args = append(args, "get", "ingresses.networking.k8s.io", "--all-namespaces", "--output", "json")
	command := exec.CommandContext(ctx, kubectl, args...) // #nosec G204 -- executable and context are operator supplied, never passed through a shell.
	command.Env = os.Environ()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	payload, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes Ingress inventory: %w: %s", err, stderr.String())
	}
	return payload, nil
}

func writeBackup(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write backup: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func printResult(target, backup string, report statusservice.IngressConfigSyncReport, changed bool) {
	payload, _ := json.MarshalIndent(map[string]any{
		"environment": target,
		"backup":      backup,
		"changed":     changed,
		"report":      report,
	}, "", "  ")
	fmt.Println(string(payload))
}

func exitError(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
