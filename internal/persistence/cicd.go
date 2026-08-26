package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/cicd"
)

type cicdScanner interface{ Scan(...any) error }

func (s *Services) ListCICDConnections(ctx context.Context, project string) ([]cicd.StoredConnection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_key, connection_key, display_name, base_url, username,
		connection_mode, environment_key, target_name, region_name, cluster_name, namespace_name, service_name, service_port, token_cipher,
		jenkins_version, last_check_status, last_check_error, last_checked_at, created_at, updated_at
		FROM cicd_connections WHERE project_key = ? ORDER BY display_name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []cicd.StoredConnection{}
	for rows.Next() {
		var item cicd.StoredConnection
		if err := scanCICDConnection(rows, &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Services) GetCICDConnection(ctx context.Context, project, key string) (cicd.StoredConnection, error) {
	var item cicd.StoredConnection
	err := scanCICDConnection(s.db.QueryRowContext(ctx, `SELECT project_key, connection_key, display_name, base_url, username,
		connection_mode, environment_key, target_name, region_name, cluster_name, namespace_name, service_name, service_port, token_cipher,
		jenkins_version, last_check_status, last_check_error, last_checked_at, created_at, updated_at
		FROM cicd_connections WHERE project_key = ? AND connection_key = ?`, project, key), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return item, os.ErrNotExist
	}
	return item, err
}

func scanCICDConnection(scanner cicdScanner, item *cicd.StoredConnection) error {
	var checked sql.NullTime
	err := scanner.Scan(&item.ProjectKey, &item.Key, &item.DisplayName, &item.BaseURL, &item.Username,
		&item.ConnectionMode, &item.EnvironmentKey, &item.TargetName, &item.Region, &item.ClusterName, &item.Namespace, &item.ServiceName, &item.ServicePort, &item.TokenCipher,
		&item.JenkinsVersion, &item.LastCheckStatus, &item.LastCheckError, &checked, &item.CreatedAt, &item.UpdatedAt)
	if checked.Valid {
		item.LastCheckedAt = checked.Time
	}
	return err
}

func (s *Services) SaveCICDConnection(ctx context.Context, item cicd.StoredConnection) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO cicd_connections
		(project_key, connection_key, display_name, base_url, username, connection_mode, environment_key, target_name, region_name, cluster_name,
		namespace_name, service_name, service_port, token_cipher, jenkins_version, last_check_status, last_check_error, last_checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE display_name=VALUES(display_name), base_url=VALUES(base_url), username=VALUES(username),
		connection_mode=VALUES(connection_mode), environment_key=VALUES(environment_key), target_name=VALUES(target_name), region_name=VALUES(region_name),
		cluster_name=VALUES(cluster_name), namespace_name=VALUES(namespace_name), service_name=VALUES(service_name), service_port=VALUES(service_port),
		token_cipher=VALUES(token_cipher), jenkins_version=VALUES(jenkins_version), last_check_status=VALUES(last_check_status),
		last_check_error=VALUES(last_check_error), last_checked_at=VALUES(last_checked_at), updated_at=CURRENT_TIMESTAMP(6)`,
		item.ProjectKey, item.Key, item.DisplayName, item.BaseURL, item.Username, item.ConnectionMode, item.EnvironmentKey, item.TargetName,
		item.Region, item.ClusterName, item.Namespace, item.ServiceName, item.ServicePort, item.TokenCipher, item.JenkinsVersion,
		item.LastCheckStatus, item.LastCheckError, nullableCICDTime(item.LastCheckedAt))
	return err
}

func (s *Services) DeleteCICDConnection(ctx context.Context, project, key string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM cicd_jobs WHERE project_key=? AND connection_key=?) +
		(SELECT COUNT(*) FROM cicd_credentials WHERE project_key=? AND connection_key=?)`, project, key, project, key).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return cicd.ErrConflict
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM cicd_connections WHERE project_key=? AND connection_key=?`, project, key)
	if err == nil {
		if affected, _ := result.RowsAffected(); affected == 0 {
			return os.ErrNotExist
		}
	}
	return err
}

func (s *Services) ListCICDCredentials(ctx context.Context, project string) ([]cicd.StoredCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_key, environment_key, credential_key, connection_key, display_name, credential_kind, external_id,
		description, secret_cipher, sync_status, sync_error, last_synced_at, created_at, updated_at
		FROM cicd_credentials WHERE project_key=? ORDER BY display_name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []cicd.StoredCredential{}
	for rows.Next() {
		var item cicd.StoredCredential
		if err := scanCICDCredential(rows, &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Services) GetCICDCredential(ctx context.Context, project, key string) (cicd.StoredCredential, error) {
	var item cicd.StoredCredential
	err := scanCICDCredential(s.db.QueryRowContext(ctx, `SELECT project_key, environment_key, credential_key, connection_key, display_name, credential_kind, external_id,
		description, secret_cipher, sync_status, sync_error, last_synced_at, created_at, updated_at
		FROM cicd_credentials WHERE project_key=? AND credential_key=?`, project, key), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return item, os.ErrNotExist
	}
	return item, err
}

func scanCICDCredential(scanner cicdScanner, item *cicd.StoredCredential) error {
	var synced sql.NullTime
	err := scanner.Scan(&item.ProjectKey, &item.EnvironmentKey, &item.Key, &item.ConnectionKey, &item.DisplayName, &item.Kind, &item.ExternalID, &item.Description,
		&item.SecretCipher, &item.SyncStatus, &item.SyncError, &synced, &item.CreatedAt, &item.UpdatedAt)
	if synced.Valid {
		item.LastSyncedAt = synced.Time
	}
	return err
}

func (s *Services) SaveCICDCredential(ctx context.Context, item cicd.StoredCredential) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO cicd_credentials
		(project_key,environment_key,credential_key,connection_key,display_name,credential_kind,external_id,description,secret_cipher,sync_status,sync_error,last_synced_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE environment_key=VALUES(environment_key),connection_key=VALUES(connection_key),display_name=VALUES(display_name),
		credential_kind=VALUES(credential_kind),external_id=VALUES(external_id),description=VALUES(description),secret_cipher=VALUES(secret_cipher),
		sync_status=VALUES(sync_status),sync_error=VALUES(sync_error),last_synced_at=VALUES(last_synced_at),updated_at=CURRENT_TIMESTAMP(6)`,
		item.ProjectKey, item.EnvironmentKey, item.Key, item.ConnectionKey, item.DisplayName, item.Kind, item.ExternalID, item.Description, item.SecretCipher, item.SyncStatus, item.SyncError, nullableCICDTime(item.LastSyncedAt))
	return err
}

func (s *Services) DeleteCICDCredential(ctx context.Context, project, key string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cicd_jobs WHERE project_key=? AND (jenkinsfile_credential=? OR manifest_credential=?)`, project, key, key).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return cicd.ErrConflict
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM cicd_credentials WHERE project_key=? AND credential_key=?`, project, key)
	if err == nil {
		if affected, _ := result.RowsAffected(); affected == 0 {
			return os.ErrNotExist
		}
	}
	return err
}

func (s *Services) ListCICDRepositories(ctx context.Context, project string) ([]cicd.Repository, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_key,repository_key,display_name,provider_name,purpose_name,clone_url,default_branch,default_path,description,created_at,updated_at FROM cicd_repositories WHERE project_key=? ORDER BY display_name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []cicd.Repository{}
	for rows.Next() {
		var item cicd.Repository
		if err := scanCICDRepository(rows, &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Services) GetCICDRepository(ctx context.Context, project, key string) (cicd.Repository, error) {
	var item cicd.Repository
	err := scanCICDRepository(s.db.QueryRowContext(ctx, `SELECT project_key,repository_key,display_name,provider_name,purpose_name,clone_url,default_branch,default_path,description,created_at,updated_at FROM cicd_repositories WHERE project_key=? AND repository_key=?`, project, key), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return item, os.ErrNotExist
	}
	return item, err
}

func scanCICDRepository(scanner cicdScanner, item *cicd.Repository) error {
	return scanner.Scan(&item.ProjectKey, &item.Key, &item.DisplayName, &item.Provider, &item.Purpose, &item.CloneURL, &item.DefaultBranch, &item.DefaultPath, &item.Description, &item.CreatedAt, &item.UpdatedAt)
}

func (s *Services) SaveCICDRepository(ctx context.Context, item cicd.Repository) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO cicd_repositories(project_key,repository_key,display_name,provider_name,purpose_name,clone_url,default_branch,default_path,description)
		VALUES(?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE display_name=VALUES(display_name),provider_name=VALUES(provider_name),purpose_name=VALUES(purpose_name),clone_url=VALUES(clone_url),default_branch=VALUES(default_branch),default_path=VALUES(default_path),description=VALUES(description),updated_at=CURRENT_TIMESTAMP(6)`,
		item.ProjectKey, item.Key, item.DisplayName, item.Provider, item.Purpose, item.CloneURL, item.DefaultBranch, item.DefaultPath, item.Description)
	return err
}

func (s *Services) DeleteCICDRepository(ctx context.Context, project, key string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cicd_jobs WHERE project_key=? AND (jenkinsfile_repository_key=? OR source_repository_key=? OR manifest_repository_key=?)`, project, key, key, key).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return cicd.ErrConflict
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM cicd_repositories WHERE project_key=? AND repository_key=?`, project, key)
	if err == nil {
		if affected, _ := result.RowsAffected(); affected == 0 {
			return os.ErrNotExist
		}
	}
	return err
}

func (s *Services) ListCICDJobs(ctx context.Context, project string) ([]cicd.Job, error) {
	rows, err := s.db.QueryContext(ctx, cicdJobSelect+` WHERE project_key=? ORDER BY display_name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []cicd.Job{}
	for rows.Next() {
		var item cicd.Job
		if err := scanCICDJob(rows, &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Services) GetCICDJob(ctx context.Context, project, key string) (cicd.Job, error) {
	var item cicd.Job
	err := scanCICDJob(s.db.QueryRowContext(ctx, cicdJobSelect+` WHERE project_key=? AND job_key=?`, project, key), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return item, os.ErrNotExist
	}
	return item, err
}

const cicdJobSelect = `SELECT project_key,job_key,environment_key,display_name,service_name,service_keys_json,language_name,jenkinsfile_mode,execution_mode,failure_policy,compact_parameters,connection_key,jenkins_job_name,enabled,trigger_mode,trigger_branch,webhook_secret_hash,
	jenkinsfile_repository_key,jenkinsfile_repo,jenkinsfile_branch,jenkinsfile_path,jenkinsfile_content,jenkinsfile_credential,source_repository_key,source_repo,manifest_repository_key,manifest_repo,manifest_branch,manifest_path,
	manifest_credential,environment_paths_json,build_command,runtime_version,parameters_json,parameter_definitions_json,sync_status,sync_error,last_synced_at,created_at,updated_at FROM cicd_jobs`

func scanCICDJob(scanner cicdScanner, item *cicd.Job) error {
	var serviceKeys, paths, parameters, parameterDefinitions []byte
	var synced sql.NullTime
	err := scanner.Scan(&item.ProjectKey, &item.Key, &item.EnvironmentKey, &item.DisplayName, &item.ServiceName, &serviceKeys, &item.Language, &item.JenkinsfileMode, &item.ExecutionMode, &item.FailurePolicy, &item.CompactParameters, &item.ConnectionKey, &item.JenkinsJobName, &item.Enabled, &item.TriggerMode, &item.TriggerBranch, &item.WebhookSecretHash,
		&item.JenkinsfileRepository, &item.JenkinsfileRepo, &item.JenkinsfileBranch, &item.JenkinsfilePath, &item.JenkinsfileContent, &item.JenkinsfileCredential, &item.SourceRepository, &item.SourceRepo, &item.ManifestRepository, &item.ManifestRepo, &item.ManifestBranch, &item.ManifestPath,
		&item.ManifestCredential, &paths, &item.BuildCommand, &item.RuntimeVersion, &parameters, &parameterDefinitions, &item.SyncStatus, &item.SyncError, &synced, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return err
	}
	if len(serviceKeys) > 0 {
		if err = json.Unmarshal(serviceKeys, &item.ServiceKeys); err != nil {
			return err
		}
	}
	if len(item.ServiceKeys) == 0 && item.ServiceName != "" {
		item.ServiceKeys = []string{item.ServiceName}
	}
	if err = json.Unmarshal(paths, &item.EnvironmentPaths); err != nil {
		return err
	}
	if err = json.Unmarshal(parameters, &item.Parameters); err != nil {
		return err
	}
	if len(parameterDefinitions) > 0 {
		if err = json.Unmarshal(parameterDefinitions, &item.ParameterDefinitions); err != nil {
			return err
		}
	}
	if synced.Valid {
		item.LastSyncedAt = synced.Time
	}
	item.WebhookConfigured = item.WebhookSecretHash != ""
	return nil
}

func (s *Services) SaveCICDJob(ctx context.Context, item cicd.Job) error {
	serviceKeys, err := json.Marshal(item.ServiceKeys)
	if err != nil {
		return err
	}
	paths, err := json.Marshal(item.EnvironmentPaths)
	if err != nil {
		return err
	}
	parameters, err := json.Marshal(item.Parameters)
	if err != nil {
		return err
	}
	parameterDefinitions, err := json.Marshal(item.ParameterDefinitions)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cicd_jobs
	(project_key,job_key,environment_key,display_name,service_name,service_keys_json,language_name,jenkinsfile_mode,execution_mode,failure_policy,compact_parameters,connection_key,jenkins_job_name,enabled,trigger_mode,trigger_branch,webhook_secret_hash,jenkinsfile_repository_key,jenkinsfile_repo,jenkinsfile_branch,jenkinsfile_path,jenkinsfile_content,jenkinsfile_credential,source_repository_key,source_repo,manifest_repository_key,manifest_repo,manifest_branch,manifest_path,manifest_credential,environment_paths_json,build_command,runtime_version,parameters_json,parameter_definitions_json,sync_status,sync_error,last_synced_at)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE environment_key=VALUES(environment_key),display_name=VALUES(display_name),service_name=VALUES(service_name),service_keys_json=VALUES(service_keys_json),language_name=VALUES(language_name),jenkinsfile_mode=VALUES(jenkinsfile_mode),execution_mode=VALUES(execution_mode),failure_policy=VALUES(failure_policy),compact_parameters=VALUES(compact_parameters),connection_key=VALUES(connection_key),jenkins_job_name=VALUES(jenkins_job_name),enabled=VALUES(enabled),trigger_mode=VALUES(trigger_mode),trigger_branch=VALUES(trigger_branch),webhook_secret_hash=VALUES(webhook_secret_hash),jenkinsfile_repository_key=VALUES(jenkinsfile_repository_key),jenkinsfile_repo=VALUES(jenkinsfile_repo),jenkinsfile_branch=VALUES(jenkinsfile_branch),jenkinsfile_path=VALUES(jenkinsfile_path),jenkinsfile_content=VALUES(jenkinsfile_content),jenkinsfile_credential=VALUES(jenkinsfile_credential),source_repository_key=VALUES(source_repository_key),source_repo=VALUES(source_repo),manifest_repository_key=VALUES(manifest_repository_key),manifest_repo=VALUES(manifest_repo),manifest_branch=VALUES(manifest_branch),manifest_path=VALUES(manifest_path),manifest_credential=VALUES(manifest_credential),environment_paths_json=VALUES(environment_paths_json),build_command=VALUES(build_command),runtime_version=VALUES(runtime_version),parameters_json=VALUES(parameters_json),parameter_definitions_json=VALUES(parameter_definitions_json),sync_status=VALUES(sync_status),sync_error=VALUES(sync_error),last_synced_at=VALUES(last_synced_at),updated_at=CURRENT_TIMESTAMP(6)`,
		item.ProjectKey, item.Key, item.EnvironmentKey, item.DisplayName, item.ServiceName, serviceKeys, item.Language, item.JenkinsfileMode, item.ExecutionMode, item.FailurePolicy, item.CompactParameters, item.ConnectionKey, item.JenkinsJobName, item.Enabled, item.TriggerMode, item.TriggerBranch, item.WebhookSecretHash, item.JenkinsfileRepository, item.JenkinsfileRepo, item.JenkinsfileBranch, item.JenkinsfilePath, item.JenkinsfileContent, item.JenkinsfileCredential, item.SourceRepository, item.SourceRepo, item.ManifestRepository, item.ManifestRepo, item.ManifestBranch, item.ManifestPath, item.ManifestCredential, paths, item.BuildCommand, item.RuntimeVersion, parameters, parameterDefinitions, item.SyncStatus, item.SyncError, nullableCICDTime(item.LastSyncedAt))
	return err
}

func (s *Services) DeleteCICDJob(ctx context.Context, project, key string) error {
	usage, err := s.GetCICDJobBuildUsage(ctx, project, key)
	if err != nil {
		return err
	}
	if usage.ActiveBuilds > 0 {
		return fmt.Errorf("%w：该 Job 仍有 %d 个排队中或运行中的构建，请先停止后再删除", cicd.ErrConflict, usage.ActiveBuilds)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM cicd_jobs WHERE project_key=? AND job_key=?`, project, key)
	if err == nil {
		if affected, _ := result.RowsAffected(); affected == 0 {
			return os.ErrNotExist
		}
	}
	return err
}

func (s *Services) GetCICDJobBuildUsage(ctx context.Context, project, key string) (cicd.JobBuildUsage, error) {
	var usage cicd.JobBuildUsage
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(status_name IN ('queued','running')),0) FROM cicd_builds WHERE project_key=? AND job_key=?`, project, key).Scan(&usage.TotalBuilds, &usage.ActiveBuilds)
	usage.HistoricalBuilds = usage.TotalBuilds - usage.ActiveBuilds
	return usage, err
}

func (s *Services) ListCICDBuilds(ctx context.Context, project, environment string, limit int) ([]cicd.Build, error) {
	query := cicdBuildSelect + ` WHERE project_key=?`
	args := []any{project}
	if environment != "" {
		query += ` AND environment_key=?`
		args = append(args, environment)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []cicd.Build{}
	for rows.Next() {
		var item cicd.Build
		if err := scanCICDBuild(rows, &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (s *Services) GetCICDBuild(ctx context.Context, project, id string) (cicd.Build, error) {
	var item cicd.Build
	err := scanCICDBuild(s.db.QueryRowContext(ctx, cicdBuildSelect+` WHERE project_key=? AND build_id=?`, project, id), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return item, os.ErrNotExist
	}
	return item, err
}

const cicdBuildSelect = `SELECT build_id,project_key,job_key,environment_key,requested_by,status_name,result_name,queue_url,build_number,build_url,parameters_json,progress_percent,current_stage,stages_json,error_text,created_at,started_at,finished_at,updated_at FROM cicd_builds`

func scanCICDBuild(scanner cicdScanner, item *cicd.Build) error {
	var parameters, stages []byte
	var started, finished sql.NullTime
	err := scanner.Scan(&item.ID, &item.ProjectKey, &item.JobKey, &item.Environment, &item.RequestedBy, &item.Status, &item.Result, &item.QueueURL, &item.BuildNumber, &item.BuildURL, &parameters, &item.Progress, &item.CurrentStage, &stages, &item.Error, &item.CreatedAt, &started, &finished, &item.UpdatedAt)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(parameters, &item.Parameters); err != nil {
		return err
	}
	if len(stages) > 0 {
		if err = json.Unmarshal(stages, &item.Stages); err != nil {
			return err
		}
	}
	item.Services = splitCICDServices(item.Parameters["TARGET_SERVICES"])
	if started.Valid {
		item.StartedAt = started.Time
	}
	if finished.Valid {
		item.FinishedAt = finished.Time
	}
	return nil
}
func (s *Services) SaveCICDBuild(ctx context.Context, item cicd.Build) error {
	parameters, err := json.Marshal(item.Parameters)
	if err != nil {
		return err
	}
	stages, err := json.Marshal(item.Stages)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cicd_builds(build_id,project_key,job_key,environment_key,requested_by,status_name,result_name,queue_url,build_number,build_url,parameters_json,progress_percent,current_stage,stages_json,error_text,created_at,started_at,finished_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE status_name=VALUES(status_name),result_name=VALUES(result_name),queue_url=VALUES(queue_url),build_number=VALUES(build_number),build_url=VALUES(build_url),parameters_json=VALUES(parameters_json),progress_percent=VALUES(progress_percent),current_stage=VALUES(current_stage),stages_json=VALUES(stages_json),error_text=VALUES(error_text),started_at=VALUES(started_at),finished_at=VALUES(finished_at),updated_at=VALUES(updated_at)`, item.ID, item.ProjectKey, item.JobKey, item.Environment, item.RequestedBy, item.Status, item.Result, item.QueueURL, item.BuildNumber, item.BuildURL, parameters, item.Progress, item.CurrentStage, stages, item.Error, item.CreatedAt, nullableCICDTime(item.StartedAt), nullableCICDTime(item.FinishedAt), time.Now().UTC())
	return err
}

func splitCICDServices(value string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] {
			result = append(result, item)
			seen[item] = true
		}
	}
	return result
}

func (s *Services) HasActiveCICDBuilds(ctx context.Context, project string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cicd_builds WHERE project_key=? AND status_name IN ('queued','running')`, project).Scan(&count)
	return count > 0, err
}

func (s *Services) DeleteCICDProject(ctx context.Context, project string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cicd_builds WHERE project_key=? AND status_name IN ('queued','running') FOR UPDATE`, project).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return cicd.ErrConflict
	}
	for _, statement := range []string{
		`DELETE FROM cicd_builds WHERE project_key=?`,
		`DELETE FROM cicd_jobs WHERE project_key=?`,
		`DELETE FROM cicd_repositories WHERE project_key=?`,
		`DELETE FROM cicd_credentials WHERE project_key=?`,
		`DELETE FROM cicd_connections WHERE project_key=?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, project); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullableCICDTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}
