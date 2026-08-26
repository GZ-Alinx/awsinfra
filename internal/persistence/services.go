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

	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"

	"github.com/GZ-Alinx/awsinfra/internal/access"
	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/auditlog"
	"github.com/GZ-Alinx/awsinfra/internal/awscredentials"
	"github.com/GZ-Alinx/awsinfra/internal/componentcatalog"
	"github.com/GZ-Alinx/awsinfra/internal/dataservicecredentials"
	"github.com/GZ-Alinx/awsinfra/internal/jobs"
	"github.com/GZ-Alinx/awsinfra/internal/staticcdn"
	"github.com/GZ-Alinx/awsinfra/internal/tlscertificates"
)

type Services struct {
	db          *sql.DB
	redis       *redis.Client
	keyPrefix   string
	statusTTL   time.Duration
	jobStateTTL time.Duration
}

func Open(ctx context.Context, config *appconfig.Config) (*Services, error) {
	dsn := strings.TrimSpace(config.MySQLDSN())
	if dsn == "" {
		return nil, fmt.Errorf("%s is not set", config.DataStore.MySQL.DSNEnv)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open MySQL: %w", err)
	}
	db.SetMaxOpenConns(config.DataStore.MySQL.MaxOpenConns)
	db.SetMaxIdleConns(config.DataStore.MySQL.MaxIdleConns)
	db.SetConnMaxLifetime(config.DataStore.MySQL.ConnMaxLife)
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("connect MySQL: %w", err), db.Close())
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     config.DataStore.Redis.Address,
		Password: config.RedisPassword(),
		DB:       config.DataStore.Redis.Database,
	})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		return nil, errors.Join(fmt.Errorf("connect Redis: %w", err), redisClient.Close(), db.Close())
	}
	services := &Services{
		db: db, redis: redisClient, keyPrefix: strings.TrimSuffix(config.DataStore.Redis.KeyPrefix, ":"),
		statusTTL: config.DataStore.Redis.StatusTTL, jobStateTTL: config.DataStore.Redis.JobStateTTL,
	}
	if err := services.migrate(ctx); err != nil {
		return nil, errors.Join(err, services.Close())
	}
	return services, nil
}

func (s *Services) Close() error {
	var result error
	if s.redis != nil {
		result = errors.Join(result, s.redis.Close())
	}
	if s.db != nil {
		result = errors.Join(result, s.db.Close())
	}
	return result
}

func (s *Services) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			migration_key VARCHAR(128) NOT NULL PRIMARY KEY,
			applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS environments (
            name VARCHAR(63) NOT NULL PRIMARY KEY,
            config_json JSON NOT NULL,
            created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
            updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS users (
            username VARCHAR(64) NOT NULL PRIMARY KEY,
            display_name VARCHAR(128) NOT NULL,
            password_hash VARCHAR(512) NOT NULL,
            is_admin BOOLEAN NOT NULL DEFAULT FALSE,
			can_manage_projects BOOLEAN NOT NULL DEFAULT FALSE,
			can_manage_users BOOLEAN NOT NULL DEFAULT FALSE,
			can_manage_credentials BOOLEAN NOT NULL DEFAULT FALSE,
			can_manage_components BOOLEAN NOT NULL DEFAULT FALSE,
			can_view_audit BOOLEAN NOT NULL DEFAULT FALSE,
            active BOOLEAN NOT NULL DEFAULT TRUE,
            created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
            updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS projects (
            project_key VARCHAR(48) NOT NULL PRIMARY KEY,
            display_name VARCHAR(128) NOT NULL,
            description VARCHAR(1000) NOT NULL DEFAULT '',
            created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
            updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS project_environments (
            project_key VARCHAR(48) NOT NULL,
            environment_key VARCHAR(8) NOT NULL,
            display_name VARCHAR(32) NOT NULL,
            target_name VARCHAR(63) NOT NULL,
            region_name VARCHAR(64) NOT NULL DEFAULT '',
            created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
            updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
            PRIMARY KEY (project_key, environment_key),
            UNIQUE KEY uq_project_environment_target (target_name),
            CONSTRAINT fk_project_environments_project FOREIGN KEY (project_key) REFERENCES projects(project_key) ON DELETE CASCADE
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS project_permissions (
            project_key VARCHAR(48) NOT NULL,
            username VARCHAR(64) NOT NULL,
			can_view BOOLEAN NOT NULL DEFAULT TRUE,
            can_deploy BOOLEAN NOT NULL DEFAULT FALSE,
            can_configure BOOLEAN NOT NULL DEFAULT FALSE,
            can_view_secrets BOOLEAN NOT NULL DEFAULT FALSE,
            created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
            updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
            PRIMARY KEY (project_key, username),
            CONSTRAINT fk_project_permissions_project FOREIGN KEY (project_key) REFERENCES projects(project_key) ON DELETE CASCADE,
            CONSTRAINT fk_project_permissions_user FOREIGN KEY (username) REFERENCES users(username) ON DELETE CASCADE
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS deployment_jobs (
            id VARCHAR(40) NOT NULL PRIMARY KEY,
            environment VARCHAR(63) NOT NULL,
            project_key VARCHAR(48) NOT NULL DEFAULT '',
            environment_key VARCHAR(8) NOT NULL DEFAULT '',
            target_name VARCHAR(63) NOT NULL DEFAULT '',
            requested_by VARCHAR(64) NOT NULL DEFAULT '',
            action VARCHAR(20) NOT NULL,
            status VARCHAR(20) NOT NULL,
            created_at DATETIME(6) NOT NULL,
            started_at DATETIME(6) NULL,
            finished_at DATETIME(6) NULL,
            error_text TEXT NOT NULL,
            log_size BIGINT NOT NULL DEFAULT 0,
            progress_json JSON NULL,
            INDEX idx_jobs_environment_created (environment, created_at),
            INDEX idx_jobs_status (status)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS audit_events (
            id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
            occurred_at DATETIME(6) NOT NULL,
            username VARCHAR(128) NOT NULL,
            method VARCHAR(12) NOT NULL,
            path VARCHAR(512) NOT NULL,
            response_status INT NOT NULL,
            remote_address VARCHAR(255) NOT NULL,
            duration_ms BIGINT NOT NULL,
            INDEX idx_audit_occurred (occurred_at),
            INDEX idx_audit_username (username)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS environment_resource_snapshots (
            project_key VARCHAR(48) NOT NULL,
            environment_key VARCHAR(8) NOT NULL,
            snapshot_json JSON NOT NULL,
            created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
            updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
            PRIMARY KEY (project_key, environment_key),
            CONSTRAINT fk_resource_snapshots_environment FOREIGN KEY (project_key, environment_key)
                REFERENCES project_environments(project_key, environment_key) ON DELETE CASCADE
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS static_cdn_resources (
			project_key VARCHAR(48) NOT NULL,
			environment_key VARCHAR(8) NOT NULL,
			bucket_name VARCHAR(63) NOT NULL,
			display_name VARCHAR(128) NOT NULL,
			region_name VARCHAR(64) NOT NULL,
			cors_origins_json JSON NOT NULL,
			distribution_id VARCHAR(32) NOT NULL DEFAULT '',
			distribution_arn VARCHAR(512) NOT NULL DEFAULT '',
			domain_name VARCHAR(253) NOT NULL DEFAULT '',
			oac_id VARCHAR(32) NOT NULL DEFAULT '',
			status_name VARCHAR(32) NOT NULL DEFAULT 'creating',
			last_error VARCHAR(2000) NOT NULL DEFAULT '',
			created_by VARCHAR(64) NOT NULL DEFAULT '',
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			PRIMARY KEY (project_key, environment_key, bucket_name),
			INDEX idx_static_cdn_distribution (distribution_id),
			CONSTRAINT fk_static_cdn_environment FOREIGN KEY (project_key, environment_key)
				REFERENCES project_environments(project_key, environment_key) ON DELETE RESTRICT
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS environment_tls_certificates (
			project_key VARCHAR(48) NOT NULL,
			environment_key VARCHAR(8) NOT NULL,
			certificate_key VARCHAR(63) NOT NULL,
			certificate_cipher MEDIUMTEXT NOT NULL,
			private_key_cipher MEDIUMTEXT NOT NULL,
			fingerprint_sha256 CHAR(64) NOT NULL,
			subject_name VARCHAR(1000) NOT NULL DEFAULT '',
			dns_names_json JSON NOT NULL,
			not_before DATETIME(6) NOT NULL,
			not_after DATETIME(6) NOT NULL,
			updated_by VARCHAR(64) NOT NULL DEFAULT '',
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			PRIMARY KEY (project_key, environment_key, certificate_key),
			CONSTRAINT fk_tls_certificates_environment FOREIGN KEY (project_key, environment_key)
				REFERENCES project_environments(project_key, environment_key) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS environment_data_service_credentials (
			project_key VARCHAR(48) NOT NULL,
			environment_key VARCHAR(8) NOT NULL,
			service_key VARCHAR(32) NOT NULL,
			username VARCHAR(64) NOT NULL,
			password_cipher TEXT NOT NULL,
			updated_by VARCHAR(64) NOT NULL DEFAULT '',
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			PRIMARY KEY (project_key, environment_key, service_key),
			CONSTRAINT fk_data_service_credentials_environment FOREIGN KEY (project_key, environment_key)
				REFERENCES project_environments(project_key, environment_key) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS project_aws_credentials (
            project_key VARCHAR(48) NOT NULL PRIMARY KEY,
            access_key_id VARCHAR(128) NOT NULL,
            secret_access_key_cipher TEXT NOT NULL,
            session_token_cipher MEDIUMTEXT NOT NULL,
            account_id VARCHAR(32) NOT NULL DEFAULT '',
            principal_arn VARCHAR(512) NOT NULL DEFAULT '',
            principal_user_id VARCHAR(256) NOT NULL DEFAULT '',
            updated_by VARCHAR(64) NOT NULL DEFAULT '',
            verified_at DATETIME(6) NOT NULL,
            created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
            updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
            CONSTRAINT fk_project_aws_credentials_project FOREIGN KEY (project_key)
                REFERENCES projects(project_key) ON DELETE CASCADE
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS aws_credentials (
            credential_key VARCHAR(63) NOT NULL PRIMARY KEY,
            display_name VARCHAR(128) NOT NULL,
            project_key VARCHAR(48) NOT NULL,
            access_key_id VARCHAR(128) NOT NULL,
            secret_access_key_cipher TEXT NOT NULL,
            session_token_cipher MEDIUMTEXT NOT NULL,
            account_id VARCHAR(32) NOT NULL DEFAULT '',
            principal_arn VARCHAR(512) NOT NULL DEFAULT '',
            principal_user_id VARCHAR(256) NOT NULL DEFAULT '',
            updated_by VARCHAR(64) NOT NULL DEFAULT '',
            verified_at DATETIME(6) NOT NULL,
            created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
            updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
            INDEX idx_aws_credentials_project (project_key),
            CONSTRAINT fk_aws_credentials_project FOREIGN KEY (project_key)
                REFERENCES projects(project_key) ON DELETE CASCADE
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS terraform_state_backend (
			config_key VARCHAR(32) NOT NULL PRIMARY KEY,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			bucket_name VARCHAR(63) NOT NULL,
			region_name VARCHAR(32) NOT NULL,
			key_prefix VARCHAR(512) NOT NULL,
			kms_key_id VARCHAR(512) NOT NULL DEFAULT '',
			access_key_id VARCHAR(128) NOT NULL,
			secret_access_key_cipher TEXT NOT NULL,
			session_token_cipher MEDIUMTEXT NOT NULL,
			account_id VARCHAR(32) NOT NULL DEFAULT '',
			principal_arn VARCHAR(512) NOT NULL DEFAULT '',
			principal_user_id VARCHAR(256) NOT NULL DEFAULT '',
			updated_by VARCHAR(64) NOT NULL DEFAULT '',
			verified_at DATETIME(6) NOT NULL,
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS helm_component_catalog (
            component_key VARCHAR(63) NOT NULL PRIMARY KEY,
            display_name VARCHAR(128) NOT NULL,
            category VARCHAR(64) NOT NULL,
            description VARCHAR(1000) NOT NULL DEFAULT '',
            repository_url VARCHAR(1000) NOT NULL,
            chart_name VARCHAR(255) NOT NULL,
            chart_version VARCHAR(128) NOT NULL DEFAULT '',
            default_namespace VARCHAR(63) NOT NULL DEFAULT 'platform-server',
            values_yaml MEDIUMTEXT NOT NULL,
            created_by VARCHAR(64) NOT NULL DEFAULT '',
            created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
            updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS gitlab_servers (
			server_key VARCHAR(63) NOT NULL PRIMARY KEY,
			display_name VARCHAR(128) NOT NULL,
			base_url VARCHAR(1000) NOT NULL,
			root_group VARCHAR(500) NOT NULL,
			root_groups_json JSON NULL,
			default_branch VARCHAR(255) NOT NULL DEFAULT 'main',
			visibility_name VARCHAR(16) NOT NULL DEFAULT 'private',
			allow_insecure_http BOOLEAN NOT NULL DEFAULT FALSE,
			token_cipher MEDIUMTEXT NOT NULL,
			last_check_status VARCHAR(32) NOT NULL DEFAULT '',
			last_check_error VARCHAR(1000) NOT NULL DEFAULT '',
			last_checked_at DATETIME(6) NULL,
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS project_gitlab_delivery (
			project_key VARCHAR(48) NOT NULL PRIMARY KEY,
			server_key VARCHAR(63) NULL,
			root_group VARCHAR(500) NOT NULL,
			source_server_key VARCHAR(63) NOT NULL DEFAULT '',
			source_root_group VARCHAR(500) NOT NULL DEFAULT '',
			default_branch VARCHAR(255) NOT NULL DEFAULT 'main',
			jenkinsfiles_project_id BIGINT NOT NULL DEFAULT 0,
			jenkinsfiles_project_path VARCHAR(700) NOT NULL,
			jenkinsfiles_clone_url VARCHAR(1000) NOT NULL DEFAULT '',
			manifests_project_id BIGINT NOT NULL DEFAULT 0,
			manifests_project_path VARCHAR(700) NOT NULL,
			manifests_clone_url VARCHAR(1000) NOT NULL DEFAULT '',
			services_json JSON NOT NULL,
			provision_status VARCHAR(32) NOT NULL DEFAULT '',
			provision_error VARCHAR(1000) NOT NULL DEFAULT '',
			last_provisioned_at DATETIME(6) NULL,
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			CONSTRAINT fk_project_gitlab_delivery_project FOREIGN KEY (project_key) REFERENCES projects(project_key) ON DELETE CASCADE,
			CONSTRAINT fk_project_gitlab_delivery_server FOREIGN KEY (server_key) REFERENCES gitlab_servers(server_key) ON DELETE RESTRICT
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS cicd_connections (
			project_key VARCHAR(48) NOT NULL,
			connection_key VARCHAR(63) NOT NULL,
			display_name VARCHAR(128) NOT NULL,
			base_url VARCHAR(1000) NOT NULL,
			username VARCHAR(128) NOT NULL,
			connection_mode VARCHAR(32) NOT NULL DEFAULT 'direct',
			environment_key VARCHAR(8) NOT NULL DEFAULT '',
			target_name VARCHAR(63) NOT NULL DEFAULT '',
			region_name VARCHAR(64) NOT NULL DEFAULT '',
			cluster_name VARCHAR(128) NOT NULL DEFAULT '',
			namespace_name VARCHAR(63) NOT NULL DEFAULT '',
			service_name VARCHAR(63) NOT NULL DEFAULT '',
			service_port INT NOT NULL DEFAULT 0,
			token_cipher MEDIUMTEXT NOT NULL,
			jenkins_version VARCHAR(128) NOT NULL DEFAULT '',
			last_check_status VARCHAR(32) NOT NULL DEFAULT '',
			last_check_error VARCHAR(1000) NOT NULL DEFAULT '',
			last_checked_at DATETIME(6) NULL,
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			PRIMARY KEY (project_key, connection_key),
			CONSTRAINT fk_cicd_connections_project FOREIGN KEY (project_key) REFERENCES projects(project_key) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS cicd_credentials (
			project_key VARCHAR(48) NOT NULL,
			environment_key VARCHAR(8) NOT NULL DEFAULT '',
			credential_key VARCHAR(63) NOT NULL,
			connection_key VARCHAR(63) NOT NULL,
			display_name VARCHAR(128) NOT NULL,
			credential_kind VARCHAR(32) NOT NULL,
			external_id VARCHAR(128) NOT NULL,
			description VARCHAR(500) NOT NULL DEFAULT '',
			secret_cipher MEDIUMTEXT NOT NULL,
			sync_status VARCHAR(32) NOT NULL DEFAULT '',
			sync_error VARCHAR(1000) NOT NULL DEFAULT '',
			last_synced_at DATETIME(6) NULL,
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			PRIMARY KEY (project_key, credential_key),
			UNIQUE KEY uq_cicd_credential_external (project_key, connection_key, external_id),
			CONSTRAINT fk_cicd_credentials_project FOREIGN KEY (project_key)
				REFERENCES projects(project_key) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS cicd_repositories (
			project_key VARCHAR(48) NOT NULL,
			repository_key VARCHAR(63) NOT NULL,
			display_name VARCHAR(128) NOT NULL,
			provider_name VARCHAR(32) NOT NULL,
			purpose_name VARCHAR(32) NOT NULL,
			clone_url VARCHAR(1000) NOT NULL,
			default_branch VARCHAR(255) NOT NULL DEFAULT 'main',
			default_path VARCHAR(500) NOT NULL DEFAULT '',
			description VARCHAR(500) NOT NULL DEFAULT '',
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			PRIMARY KEY (project_key, repository_key),
			CONSTRAINT fk_cicd_repositories_project FOREIGN KEY (project_key)
				REFERENCES projects(project_key) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS cicd_jobs (
			project_key VARCHAR(48) NOT NULL,
			job_key VARCHAR(63) NOT NULL,
			environment_key VARCHAR(8) NOT NULL DEFAULT '',
			display_name VARCHAR(128) NOT NULL,
			service_name VARCHAR(128) NOT NULL,
			service_keys_json JSON NULL,
			language_name VARCHAR(16) NOT NULL,
			jenkinsfile_mode VARCHAR(24) NOT NULL DEFAULT 'existing',
			execution_mode VARCHAR(24) NOT NULL DEFAULT 'serial',
			failure_policy VARCHAR(24) NOT NULL DEFAULT 'stop',
			compact_parameters BOOLEAN NOT NULL DEFAULT FALSE,
			connection_key VARCHAR(63) NOT NULL,
			jenkins_job_name VARCHAR(128) NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			trigger_mode VARCHAR(24) NOT NULL DEFAULT 'manual',
			trigger_branch VARCHAR(255) NOT NULL DEFAULT '',
			webhook_secret_hash VARCHAR(64) NOT NULL DEFAULT '',
			jenkinsfile_repository_key VARCHAR(63) NOT NULL DEFAULT '',
			jenkinsfile_repo VARCHAR(1000) NOT NULL,
			jenkinsfile_branch VARCHAR(255) NOT NULL,
			jenkinsfile_path VARCHAR(500) NOT NULL,
			jenkinsfile_content MEDIUMTEXT NOT NULL,
			jenkinsfile_credential VARCHAR(63) NOT NULL DEFAULT '',
			source_repository_key VARCHAR(63) NOT NULL DEFAULT '',
			source_repo VARCHAR(1000) NOT NULL DEFAULT '',
			manifest_repository_key VARCHAR(63) NOT NULL DEFAULT '',
			manifest_repo VARCHAR(1000) NOT NULL,
			manifest_branch VARCHAR(255) NOT NULL,
			manifest_path VARCHAR(500) NOT NULL,
			manifest_credential VARCHAR(63) NOT NULL DEFAULT '',
			environment_paths_json JSON NOT NULL,
			build_command VARCHAR(1000) NOT NULL DEFAULT '',
			runtime_version VARCHAR(64) NOT NULL DEFAULT '',
			parameters_json JSON NOT NULL,
			parameter_definitions_json JSON NULL,
			sync_status VARCHAR(32) NOT NULL DEFAULT '',
			sync_error VARCHAR(1000) NOT NULL DEFAULT '',
			last_synced_at DATETIME(6) NULL,
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			PRIMARY KEY (project_key, job_key),
			UNIQUE KEY uq_cicd_jenkins_job (project_key, connection_key, jenkins_job_name),
			CONSTRAINT fk_cicd_jobs_project FOREIGN KEY (project_key)
				REFERENCES projects(project_key) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS cicd_builds (
			build_id VARCHAR(64) NOT NULL PRIMARY KEY,
			project_key VARCHAR(48) NOT NULL,
			job_key VARCHAR(63) NOT NULL,
			environment_key VARCHAR(8) NOT NULL,
			requested_by VARCHAR(64) NOT NULL,
			status_name VARCHAR(32) NOT NULL,
			result_name VARCHAR(32) NOT NULL DEFAULT '',
			queue_url VARCHAR(1000) NOT NULL DEFAULT '',
			build_number BIGINT NOT NULL DEFAULT 0,
			build_url VARCHAR(1000) NOT NULL DEFAULT '',
			parameters_json JSON NOT NULL,
			progress_percent INT NOT NULL DEFAULT 0,
			current_stage VARCHAR(255) NOT NULL DEFAULT '',
			stages_json JSON NULL,
			error_text VARCHAR(1000) NOT NULL DEFAULT '',
			created_at DATETIME(6) NOT NULL,
			started_at DATETIME(6) NULL,
			finished_at DATETIME(6) NULL,
			updated_at DATETIME(6) NOT NULL,
			INDEX idx_cicd_build_scope (project_key, environment_key, created_at),
			INDEX idx_cicd_build_job (project_key, job_key, created_at),
			CONSTRAINT fk_cicd_build_project FOREIGN KEY (project_key)
				REFERENCES projects(project_key) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("run MySQL migration: %w", err)
		}
	}
	if err := s.ensureProjectGitLabDeliveryDetachable(ctx); err != nil {
		return err
	}
	columns := map[string]string{
		"project_key":     "VARCHAR(48) NOT NULL DEFAULT '' AFTER environment",
		"environment_key": "VARCHAR(8) NOT NULL DEFAULT '' AFTER project_key",
		"target_name":     "VARCHAR(63) NOT NULL DEFAULT '' AFTER environment_key",
		"requested_by":    "VARCHAR(64) NOT NULL DEFAULT '' AFTER target_name",
		"progress_json":   "JSON NULL AFTER log_size",
	}
	for name, definition := range columns {
		if err := s.ensureColumn(ctx, "deployment_jobs", name, definition); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "project_permissions", "can_view_secrets", "BOOLEAN NOT NULL DEFAULT FALSE AFTER can_configure"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "project_permissions", "can_view", "BOOLEAN NOT NULL DEFAULT TRUE AFTER username"); err != nil {
		return err
	}
	userPermissionColumns := []struct{ name, definition string }{
		{"can_manage_projects", "BOOLEAN NOT NULL DEFAULT FALSE AFTER is_admin"},
		{"can_manage_users", "BOOLEAN NOT NULL DEFAULT FALSE AFTER can_manage_projects"},
		{"can_manage_credentials", "BOOLEAN NOT NULL DEFAULT FALSE AFTER can_manage_users"},
		{"can_manage_components", "BOOLEAN NOT NULL DEFAULT FALSE AFTER can_manage_credentials"},
		{"can_view_audit", "BOOLEAN NOT NULL DEFAULT FALSE AFTER can_manage_components"},
	}
	for _, column := range userPermissionColumns {
		if err := s.ensureColumn(ctx, "users", column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET can_manage_projects = TRUE, can_manage_users = TRUE, can_manage_credentials = TRUE, can_manage_components = TRUE, can_view_audit = TRUE WHERE is_admin = TRUE`); err != nil {
		return fmt.Errorf("backfill administrator platform permissions: %w", err)
	}
	if err := s.ensureColumn(ctx, "projects", "selected_aws_credential_key", "VARCHAR(63) NOT NULL DEFAULT '' AFTER description"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "projects", "deleted_at", "DATETIME(6) NULL AFTER selected_aws_credential_key"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "helm_component_catalog", "replica_paths_json", "JSON NULL AFTER default_namespace"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "gitlab_servers", "root_groups_json", "JSON NULL AFTER root_group"); err != nil {
		return err
	}
	cicdConnectionColumns := []struct{ name, definition string }{
		{"connection_mode", "VARCHAR(32) NOT NULL DEFAULT 'direct' AFTER username"},
		{"environment_key", "VARCHAR(8) NOT NULL DEFAULT '' AFTER connection_mode"},
		{"target_name", "VARCHAR(63) NOT NULL DEFAULT '' AFTER environment_key"},
		{"region_name", "VARCHAR(64) NOT NULL DEFAULT '' AFTER target_name"},
		{"cluster_name", "VARCHAR(128) NOT NULL DEFAULT '' AFTER region_name"},
		{"namespace_name", "VARCHAR(63) NOT NULL DEFAULT '' AFTER cluster_name"},
		{"service_name", "VARCHAR(63) NOT NULL DEFAULT '' AFTER namespace_name"},
		{"service_port", "INT NOT NULL DEFAULT 0 AFTER service_name"},
	}
	for _, column := range cicdConnectionColumns {
		if err := s.ensureColumn(ctx, "cicd_connections", column.name, column.definition); err != nil {
			return err
		}
	}
	cicdJobRepositoryColumns := []struct{ name, definition string }{
		{"jenkinsfile_repository_key", "VARCHAR(63) NOT NULL DEFAULT '' AFTER enabled"},
		{"source_repository_key", "VARCHAR(63) NOT NULL DEFAULT '' AFTER jenkinsfile_credential"},
		{"manifest_repository_key", "VARCHAR(63) NOT NULL DEFAULT '' AFTER source_repo"},
	}
	if err := s.ensureColumn(ctx, "cicd_jobs", "environment_key", "VARCHAR(8) NOT NULL DEFAULT '' AFTER job_key"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE cicd_jobs j
		LEFT JOIN cicd_connections c
			ON c.project_key = j.project_key AND c.connection_key = j.connection_key
		SET j.environment_key = CASE
			WHEN c.environment_key IN ('dev','test','uat','prod') THEN c.environment_key
			WHEN JSON_UNQUOTE(JSON_EXTRACT(j.parameters_json, '$.DEPLOY_ENV')) IN ('dev','test','uat','prod')
				THEN JSON_UNQUOTE(JSON_EXTRACT(j.parameters_json, '$.DEPLOY_ENV'))
			ELSE 'dev'
		END
		WHERE j.environment_key = ''`); err != nil {
		return fmt.Errorf("backfill CI/CD Job environments: %w", err)
	}
	if err := s.ensureIndex(ctx, "cicd_jobs", "idx_cicd_jobs_environment", "(project_key, environment_key, updated_at)"); err != nil {
		return err
	}
	// Bind legacy connections only when every referencing Job belongs to one
	// unambiguous environment. Mixed test/prod connections intentionally stay
	// unbound and are blocked by the CI/CD service until the operator creates
	// separate environment connections.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE cicd_connections c
		JOIN (
			SELECT project_key, connection_key, MIN(environment_key) AS environment_key
			FROM cicd_jobs
			WHERE environment_key IN ('dev','test','uat','prod')
			GROUP BY project_key, connection_key
			HAVING COUNT(DISTINCT environment_key) = 1
		) j ON j.project_key = c.project_key AND j.connection_key = c.connection_key
		SET c.environment_key = j.environment_key
		WHERE c.environment_key = ''`); err != nil {
		return fmt.Errorf("backfill unambiguous CI/CD connection environments: %w", err)
	}
	if err := s.ensureColumn(ctx, "cicd_credentials", "environment_key", "VARCHAR(8) NOT NULL DEFAULT '' AFTER project_key"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE cicd_credentials cr
		JOIN cicd_connections cn
			ON cn.project_key = cr.project_key AND cn.connection_key = cr.connection_key
		SET cr.environment_key = cn.environment_key
		WHERE cr.environment_key = '' AND cn.environment_key IN ('dev','test','uat','prod')`); err != nil {
		return fmt.Errorf("backfill CI/CD credential environments: %w", err)
	}
	if err := s.ensureIndex(ctx, "cicd_credentials", "idx_cicd_credentials_environment", "(project_key, environment_key, updated_at)"); err != nil {
		return err
	}
	for _, column := range cicdJobRepositoryColumns {
		if err := s.ensureColumn(ctx, "cicd_jobs", column.name, column.definition); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "cicd_jobs", "parameter_definitions_json", "JSON NULL AFTER parameters_json"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "cicd_jobs", "jenkinsfile_content", "MEDIUMTEXT NOT NULL AFTER jenkinsfile_path"); err != nil {
		return err
	}
	cicdJobMultiServiceColumns := []struct{ name, definition string }{
		{"service_keys_json", "JSON NULL AFTER service_name"},
		{"jenkinsfile_mode", "VARCHAR(24) NOT NULL DEFAULT 'existing' AFTER language_name"},
		{"execution_mode", "VARCHAR(24) NOT NULL DEFAULT 'serial' AFTER jenkinsfile_mode"},
		{"failure_policy", "VARCHAR(24) NOT NULL DEFAULT 'stop' AFTER execution_mode"},
		{"compact_parameters", "BOOLEAN NOT NULL DEFAULT FALSE AFTER failure_policy"},
	}
	for _, column := range cicdJobMultiServiceColumns {
		if err := s.ensureColumn(ctx, "cicd_jobs", column.name, column.definition); err != nil {
			return err
		}
	}
	cicdJobTriggerColumns := []struct{ name, definition string }{
		{"trigger_mode", "VARCHAR(24) NOT NULL DEFAULT 'manual' AFTER enabled"},
		{"trigger_branch", "VARCHAR(255) NOT NULL DEFAULT '' AFTER trigger_mode"},
		{"webhook_secret_hash", "VARCHAR(64) NOT NULL DEFAULT '' AFTER trigger_branch"},
	}
	for _, column := range cicdJobTriggerColumns {
		if err := s.ensureColumn(ctx, "cicd_jobs", column.name, column.definition); err != nil {
			return err
		}
	}
	cicdBuildProgressColumns := []struct{ name, definition string }{
		{"progress_percent", "INT NOT NULL DEFAULT 0 AFTER parameters_json"},
		{"current_stage", "VARCHAR(255) NOT NULL DEFAULT '' AFTER progress_percent"},
		{"stages_json", "JSON NULL AFTER current_stage"},
	}
	for _, column := range cicdBuildProgressColumns {
		if err := s.ensureColumn(ctx, "cicd_builds", column.name, column.definition); err != nil {
			return err
		}
	}
	gitLabDeliverySourceColumns := []struct{ name, definition string }{
		{"source_server_key", "VARCHAR(63) NOT NULL DEFAULT '' AFTER root_group"},
		{"source_root_group", "VARCHAR(500) NOT NULL DEFAULT '' AFTER source_server_key"},
	}
	for _, column := range gitLabDeliverySourceColumns {
		if err := s.ensureColumn(ctx, "project_gitlab_delivery", column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `
        INSERT IGNORE INTO aws_credentials (
            credential_key, display_name, project_key, access_key_id, secret_access_key_cipher,
            session_token_cipher, account_id, principal_arn, principal_user_id, updated_by,
            verified_at, created_at, updated_at
        )
        SELECT CONCAT(project_key, '-default'), '默认 AWS 身份', project_key, access_key_id,
            secret_access_key_cipher, session_token_cipher, account_id, principal_arn,
            principal_user_id, updated_by, verified_at, created_at, updated_at
        FROM project_aws_credentials`); err != nil {
		return fmt.Errorf("migrate project AWS credentials: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
        UPDATE projects p
        INNER JOIN aws_credentials c ON c.project_key = p.project_key
            AND c.credential_key = CONCAT(p.project_key, '-default')
        SET p.selected_aws_credential_key = c.credential_key
        WHERE p.selected_aws_credential_key = ''`); err != nil {
		return fmt.Errorf("select migrated AWS credentials: %w", err)
	}
	// Repair any historical cross-project selection before strict credential
	// isolation is applied. A credential is selectable only by its owner project.
	if _, err := s.db.ExecContext(ctx, `
        UPDATE projects p
        LEFT JOIN aws_credentials c
            ON c.credential_key = p.selected_aws_credential_key
            AND c.project_key = p.project_key
        SET p.selected_aws_credential_key = ''
        WHERE p.selected_aws_credential_key <> '' AND c.credential_key IS NULL`); err != nil {
		return fmt.Errorf("repair cross-project AWS credential selections: %w", err)
	}
	// The legacy table must be emptied after the idempotent copy; otherwise a
	// deliberately deleted default credential would be recreated on restart.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM project_aws_credentials`); err != nil {
		return fmt.Errorf("retire legacy project AWS credentials: %w", err)
	}
	if err := s.ensureIndex(ctx, "deployment_jobs", "idx_jobs_project_environment_created", "(project_key, environment_key, created_at)"); err != nil {
		return err
	}
	if err := s.runOnceMigration(ctx, "20260715-explicit-project-membership", `
		INSERT IGNORE INTO project_permissions (
			project_key, username, can_view, can_deploy, can_configure, can_view_secrets
		)
		SELECT p.project_key, u.username, TRUE, TRUE, TRUE, TRUE
		FROM projects p CROSS JOIN users u WHERE u.is_admin = TRUE`); err != nil {
		return err
	}
	return nil
}

func (s *Services) ensureProjectGitLabDeliveryDetachable(ctx context.Context) error {
	var nullable string
	if err := s.db.QueryRowContext(ctx, `
		SELECT IS_NULLABLE
		FROM information_schema.columns
		WHERE table_schema = DATABASE()
			AND table_name = 'project_gitlab_delivery'
			AND column_name = 'server_key'`).Scan(&nullable); err != nil {
		return fmt.Errorf("inspect project GitLab binding schema: %w", err)
	}
	var foreignKeyCount int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM information_schema.table_constraints
		WHERE constraint_schema = DATABASE()
			AND table_name = 'project_gitlab_delivery'
			AND constraint_name = 'fk_project_gitlab_delivery_server'
			AND constraint_type = 'FOREIGN KEY'`).Scan(&foreignKeyCount); err != nil {
		return fmt.Errorf("inspect project GitLab binding foreign key: %w", err)
	}
	if nullable != "YES" {
		if foreignKeyCount > 0 {
			if _, err := s.db.ExecContext(ctx, `ALTER TABLE project_gitlab_delivery DROP FOREIGN KEY fk_project_gitlab_delivery_server`); err != nil {
				return fmt.Errorf("unlock project GitLab binding foreign key: %w", err)
			}
			foreignKeyCount = 0
		}
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE project_gitlab_delivery MODIFY server_key VARCHAR(63) NULL`); err != nil {
			return fmt.Errorf("allow detached project GitLab binding: %w", err)
		}
	}
	if foreignKeyCount == 0 {
		if _, err := s.db.ExecContext(ctx, `
			ALTER TABLE project_gitlab_delivery
			ADD CONSTRAINT fk_project_gitlab_delivery_server
			FOREIGN KEY (server_key) REFERENCES gitlab_servers(server_key) ON DELETE RESTRICT`); err != nil {
			return fmt.Errorf("restore project GitLab binding foreign key: %w", err)
		}
	}
	return nil
}

func (s *Services) runOnceMigration(ctx context.Context, key, statement string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT IGNORE INTO schema_migrations (migration_key) VALUES (?)`, key)
	if err != nil {
		return fmt.Errorf("claim migration %s: %w", key, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("run migration %s: %w", key, err)
		}
	}
	return tx.Commit()
}

func (s *Services) ensureColumn(ctx context.Context, table, column, definition string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN `%s` %s", table, column, definition)); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func (s *Services) ensureIndex(ctx context.Context, table, index, definition string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`, table, index).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("CREATE INDEX `%s` ON `%s` %s", index, table, definition)); err != nil {
		return fmt.Errorf("create index %s: %w", index, err)
	}
	return nil
}

func (s *Services) LoadEnvironments(ctx context.Context) (map[string][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, config_json FROM environments ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]byte)
	for rows.Next() {
		var name string
		var payload []byte
		if err := rows.Scan(&name, &payload); err != nil {
			return nil, err
		}
		result[name] = append([]byte(nil), payload...)
	}
	return result, rows.Err()
}

func (s *Services) GetEnvironment(ctx context.Context, name string) ([]byte, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT config_json FROM environments WHERE name = ?`, name).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), payload...), err
}

func (s *Services) SaveEnvironment(ctx context.Context, name string, payload []byte) error {
	if !json.Valid(payload) {
		return errors.New("environment payload is not valid JSON")
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO environments (name, config_json) VALUES (?, ?)
        ON DUPLICATE KEY UPDATE config_json = VALUES(config_json), updated_at = CURRENT_TIMESTAMP(6)`, name, payload)
	return err
}

func (s *Services) DeleteEnvironment(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM environments WHERE name = ?`, name)
	return err
}

func (s *Services) EnsureAdmin(ctx context.Context, user access.User) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (
			username, display_name, password_hash, is_admin, can_manage_projects,
			can_manage_users, can_manage_credentials, can_manage_components, can_view_audit, active
		) VALUES (?, ?, ?, TRUE, TRUE, TRUE, TRUE, TRUE, TRUE, TRUE)
		ON DUPLICATE KEY UPDATE is_admin = TRUE,
			can_manage_projects = TRUE, can_manage_users = TRUE,
			can_manage_credentials = TRUE, can_manage_components = TRUE, can_view_audit = TRUE, active = TRUE`,
		user.Username, user.DisplayName, user.PasswordHash)
	return err
}

func (s *Services) GetUser(ctx context.Context, username string) (access.User, error) {
	var user access.User
	err := s.db.QueryRowContext(ctx, `
		SELECT username, display_name, password_hash, is_admin, can_manage_projects,
			can_manage_users, can_manage_credentials, can_manage_components, can_view_audit, active, created_at, updated_at
        FROM users WHERE username = ?`, username).Scan(
		&user.Username, &user.DisplayName, &user.PasswordHash, &user.IsAdmin,
		&user.PlatformPermissions.CanManageProjects, &user.PlatformPermissions.CanManageUsers,
		&user.PlatformPermissions.CanManageCredentials, &user.PlatformPermissions.CanManageComponents,
		&user.PlatformPermissions.CanViewAudit,
		&user.Active, &user.CreatedAt, &user.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return access.User{}, os.ErrNotExist
	}
	if err != nil {
		return access.User{}, err
	}
	permissions, err := s.permissionsForUser(ctx, username)
	if err != nil {
		return access.User{}, err
	}
	user.Permissions = permissions
	return user, nil
}

func (s *Services) ListUsers(ctx context.Context) ([]access.User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT username, display_name, password_hash, is_admin, can_manage_projects,
			can_manage_users, can_manage_credentials, can_manage_components, can_view_audit, active, created_at, updated_at
        FROM users ORDER BY is_admin DESC, username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]access.User, 0)
	for rows.Next() {
		var user access.User
		if err := rows.Scan(&user.Username, &user.DisplayName, &user.PasswordHash, &user.IsAdmin,
			&user.PlatformPermissions.CanManageProjects, &user.PlatformPermissions.CanManageUsers,
			&user.PlatformPermissions.CanManageCredentials, &user.PlatformPermissions.CanManageComponents,
			&user.PlatformPermissions.CanViewAudit,
			&user.Active, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	permissionRows, err := s.db.QueryContext(ctx, `SELECT project_key, username, can_view, can_deploy, can_configure, can_view_secrets FROM project_permissions ORDER BY project_key`)
	if err != nil {
		return nil, err
	}
	defer permissionRows.Close()
	byUser := make(map[string][]access.Permission)
	for permissionRows.Next() {
		var permission access.Permission
		if err := permissionRows.Scan(&permission.ProjectKey, &permission.Username, &permission.CanView, &permission.CanDeploy, &permission.CanConfigure, &permission.CanViewSecrets); err != nil {
			return nil, err
		}
		byUser[permission.Username] = append(byUser[permission.Username], permission)
	}
	for index := range users {
		users[index].Permissions = byUser[users[index].Username]
		if users[index].Permissions == nil {
			users[index].Permissions = make([]access.Permission, 0)
		}
	}
	return users, permissionRows.Err()
}

func (s *Services) SaveUser(ctx context.Context, user access.User) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (
			username, display_name, password_hash, is_admin, can_manage_projects,
			can_manage_users, can_manage_credentials, can_manage_components, can_view_audit, active
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), password_hash = VALUES(password_hash),
			is_admin = VALUES(is_admin), can_manage_projects = VALUES(can_manage_projects),
			can_manage_users = VALUES(can_manage_users), can_manage_credentials = VALUES(can_manage_credentials),
			can_manage_components = VALUES(can_manage_components), can_view_audit = VALUES(can_view_audit), active = VALUES(active),
			updated_at = CURRENT_TIMESTAMP(6)`,
		user.Username, user.DisplayName, user.PasswordHash, user.IsAdmin,
		user.PlatformPermissions.CanManageProjects, user.PlatformPermissions.CanManageUsers,
		user.PlatformPermissions.CanManageCredentials, user.PlatformPermissions.CanManageComponents,
		user.PlatformPermissions.CanViewAudit, user.Active)
	return err
}

func (s *Services) DeleteUser(ctx context.Context, username string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE username = ?`, username)
	return err
}

func (s *Services) ListTLSCertificates(ctx context.Context, project, environment string) ([]tlscertificates.StoredMaterial, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_key, environment_key, certificate_key, certificate_cipher, private_key_cipher,
			fingerprint_sha256, subject_name, dns_names_json, not_before, not_after, updated_by, created_at, updated_at
		FROM environment_tls_certificates
		WHERE project_key = ? AND environment_key = ?
		ORDER BY certificate_key`, project, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]tlscertificates.StoredMaterial, 0)
	for rows.Next() {
		var record tlscertificates.StoredMaterial
		if err := scanTLSCertificate(rows, &record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Services) GetTLSCertificate(ctx context.Context, project, environment, key string) (tlscertificates.StoredMaterial, error) {
	var record tlscertificates.StoredMaterial
	err := scanTLSCertificate(s.db.QueryRowContext(ctx, `
		SELECT project_key, environment_key, certificate_key, certificate_cipher, private_key_cipher,
			fingerprint_sha256, subject_name, dns_names_json, not_before, not_after, updated_by, created_at, updated_at
		FROM environment_tls_certificates
		WHERE project_key = ? AND environment_key = ? AND certificate_key = ?`, project, environment, key), &record)
	if errors.Is(err, sql.ErrNoRows) {
		return tlscertificates.StoredMaterial{}, os.ErrNotExist
	}
	return record, err
}

type tlsCertificateScanner interface{ Scan(...any) error }

func scanTLSCertificate(scanner tlsCertificateScanner, record *tlscertificates.StoredMaterial) error {
	var dnsNamesJSON []byte
	if err := scanner.Scan(
		&record.ProjectKey, &record.EnvironmentKey, &record.Key, &record.CertificateCipher, &record.PrivateKeyCipher,
		&record.Fingerprint, &record.Subject, &dnsNamesJSON, &record.NotBefore, &record.NotAfter,
		&record.UpdatedBy, &record.CreatedAt, &record.UpdatedAt,
	); err != nil {
		return err
	}
	if err := json.Unmarshal(dnsNamesJSON, &record.DNSNames); err != nil {
		return fmt.Errorf("decode TLS certificate DNS names: %w", err)
	}
	return nil
}

func (s *Services) SaveTLSCertificate(ctx context.Context, record tlscertificates.StoredMaterial) error {
	dnsNamesJSON, err := json.Marshal(record.DNSNames)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO environment_tls_certificates (
			project_key, environment_key, certificate_key, certificate_cipher, private_key_cipher,
			fingerprint_sha256, subject_name, dns_names_json, not_before, not_after, updated_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			certificate_cipher = VALUES(certificate_cipher), private_key_cipher = VALUES(private_key_cipher),
			fingerprint_sha256 = VALUES(fingerprint_sha256), subject_name = VALUES(subject_name),
			dns_names_json = VALUES(dns_names_json), not_before = VALUES(not_before), not_after = VALUES(not_after),
			updated_by = VALUES(updated_by), updated_at = CURRENT_TIMESTAMP(6)`,
		record.ProjectKey, record.EnvironmentKey, record.Key, record.CertificateCipher, record.PrivateKeyCipher,
		record.Fingerprint, record.Subject, dnsNamesJSON, record.NotBefore, record.NotAfter, record.UpdatedBy,
	)
	return err
}

func (s *Services) DeleteTLSCertificate(ctx context.Context, project, environment, key string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM environment_tls_certificates WHERE project_key = ? AND environment_key = ? AND certificate_key = ?`, project, environment, key)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return os.ErrNotExist
	}
	return nil
}

func (s *Services) ListDataServiceCredentials(ctx context.Context, project, environment string) ([]dataservicecredentials.StoredCredential, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_key, environment_key, service_key, username, password_cipher, updated_by, created_at, updated_at
		FROM environment_data_service_credentials
		WHERE project_key = ? AND environment_key = ?
		ORDER BY service_key`, project, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]dataservicecredentials.StoredCredential, 0)
	for rows.Next() {
		var record dataservicecredentials.StoredCredential
		if err := scanDataServiceCredential(rows, &record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Services) GetDataServiceCredential(ctx context.Context, project, environment, service string) (dataservicecredentials.StoredCredential, error) {
	var record dataservicecredentials.StoredCredential
	err := scanDataServiceCredential(s.db.QueryRowContext(ctx, `
		SELECT project_key, environment_key, service_key, username, password_cipher, updated_by, created_at, updated_at
		FROM environment_data_service_credentials
		WHERE project_key = ? AND environment_key = ? AND service_key = ?`, project, environment, service), &record)
	if errors.Is(err, sql.ErrNoRows) {
		return dataservicecredentials.StoredCredential{}, os.ErrNotExist
	}
	return record, err
}

type dataServiceCredentialScanner interface{ Scan(...any) error }

func scanDataServiceCredential(scanner dataServiceCredentialScanner, record *dataservicecredentials.StoredCredential) error {
	return scanner.Scan(
		&record.ProjectKey, &record.EnvironmentKey, &record.ServiceKey, &record.Username,
		&record.PasswordCipher, &record.UpdatedBy, &record.CreatedAt, &record.UpdatedAt,
	)
}

func (s *Services) SaveDataServiceCredential(ctx context.Context, record dataservicecredentials.StoredCredential) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO environment_data_service_credentials (
			project_key, environment_key, service_key, username, password_cipher, updated_by
		) VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE username = VALUES(username), password_cipher = VALUES(password_cipher),
			updated_by = VALUES(updated_by), updated_at = CURRENT_TIMESTAMP(6)`,
		record.ProjectKey, record.EnvironmentKey, record.ServiceKey, record.Username, record.PasswordCipher, record.UpdatedBy)
	return err
}

func (s *Services) DeleteDataServiceCredential(ctx context.Context, project, environment, service string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM environment_data_service_credentials WHERE project_key = ? AND environment_key = ? AND service_key = ?`, project, environment, service)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return os.ErrNotExist
	}
	return nil
}

func (s *Services) ListAWSCredentials(ctx context.Context) ([]awscredentials.StoredCredential, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT c.credential_key, c.display_name, c.project_key, c.access_key_id,
            c.secret_access_key_cipher, c.session_token_cipher, c.account_id, c.principal_arn,
            c.principal_user_id, c.updated_by, c.verified_at, c.created_at, c.updated_at,
            (p.selected_aws_credential_key = c.credential_key) AS selected,
			(p.deleted_at IS NOT NULL) AS project_archived
        FROM aws_credentials c
        INNER JOIN projects p ON p.project_key = c.project_key
        ORDER BY c.project_key, c.display_name, c.credential_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]awscredentials.StoredCredential, 0)
	for rows.Next() {
		var record awscredentials.StoredCredential
		if err := scanAWSCredential(rows, &record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Services) GetAWSCredential(ctx context.Context, key string) (awscredentials.StoredCredential, error) {
	var record awscredentials.StoredCredential
	row := s.db.QueryRowContext(ctx, `
        SELECT c.credential_key, c.display_name, c.project_key, c.access_key_id,
            c.secret_access_key_cipher, c.session_token_cipher, c.account_id, c.principal_arn,
            c.principal_user_id, c.updated_by, c.verified_at, c.created_at, c.updated_at,
            (p.selected_aws_credential_key = c.credential_key) AS selected,
			(p.deleted_at IS NOT NULL) AS project_archived
        FROM aws_credentials c
        INNER JOIN projects p ON p.project_key = c.project_key
        WHERE c.credential_key = ?`, key)
	if err := scanAWSCredential(row, &record); errors.Is(err, sql.ErrNoRows) {
		return awscredentials.StoredCredential{}, os.ErrNotExist
	} else if err != nil {
		return awscredentials.StoredCredential{}, err
	}
	return record, nil
}

func (s *Services) GetSelectedAWSCredential(ctx context.Context, project string) (awscredentials.StoredCredential, error) {
	var record awscredentials.StoredCredential
	row := s.db.QueryRowContext(ctx, `
        SELECT c.credential_key, c.display_name, c.project_key, c.access_key_id,
            c.secret_access_key_cipher, c.session_token_cipher, c.account_id, c.principal_arn,
            c.principal_user_id, c.updated_by, c.verified_at, c.created_at, c.updated_at, TRUE, FALSE
        FROM projects p
        INNER JOIN aws_credentials c ON c.credential_key = p.selected_aws_credential_key
            AND c.project_key = p.project_key
        WHERE p.project_key = ? AND p.deleted_at IS NULL`, project)
	if err := scanAWSCredential(row, &record); errors.Is(err, sql.ErrNoRows) {
		return awscredentials.StoredCredential{}, os.ErrNotExist
	} else if err != nil {
		return awscredentials.StoredCredential{}, err
	}
	return record, nil
}

type awsCredentialScanner interface{ Scan(...any) error }

func scanAWSCredential(scanner awsCredentialScanner, record *awscredentials.StoredCredential) error {
	return scanner.Scan(
		&record.Key, &record.DisplayName, &record.ProjectKey, &record.AccessKeyID,
		&record.SecretAccessKeyCipher, &record.SessionTokenCipher, &record.AccountID,
		&record.PrincipalARN, &record.PrincipalUserID, &record.UpdatedBy, &record.VerifiedAt,
		&record.CreatedAt, &record.UpdatedAt, &record.Selected, &record.ProjectArchived,
	)
}

func (s *Services) SaveAWSCredential(ctx context.Context, record awscredentials.StoredCredential) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO aws_credentials (
            credential_key, display_name, project_key, access_key_id, secret_access_key_cipher,
            session_token_cipher, account_id, principal_arn, principal_user_id, updated_by, verified_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON DUPLICATE KEY UPDATE
			display_name = IF(project_key = VALUES(project_key), VALUES(display_name), display_name),
			access_key_id = IF(project_key = VALUES(project_key), VALUES(access_key_id), access_key_id),
			secret_access_key_cipher = IF(project_key = VALUES(project_key), VALUES(secret_access_key_cipher), secret_access_key_cipher),
			session_token_cipher = IF(project_key = VALUES(project_key), VALUES(session_token_cipher), session_token_cipher),
			account_id = IF(project_key = VALUES(project_key), VALUES(account_id), account_id),
			principal_arn = IF(project_key = VALUES(project_key), VALUES(principal_arn), principal_arn),
			principal_user_id = IF(project_key = VALUES(project_key), VALUES(principal_user_id), principal_user_id),
			updated_by = IF(project_key = VALUES(project_key), VALUES(updated_by), updated_by),
			verified_at = IF(project_key = VALUES(project_key), VALUES(verified_at), verified_at),
			updated_at = IF(project_key = VALUES(project_key), CURRENT_TIMESTAMP(6), updated_at)`,
		record.Key, record.DisplayName, record.ProjectKey, record.AccessKeyID,
		record.SecretAccessKeyCipher, record.SessionTokenCipher, record.AccountID,
		record.PrincipalARN, record.PrincipalUserID, record.UpdatedBy, record.VerifiedAt,
	)
	if err != nil {
		return err
	}
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT project_key FROM aws_credentials WHERE credential_key = ?`, record.Key).Scan(&owner); err != nil {
		return err
	}
	if owner != record.ProjectKey {
		return awscredentials.ErrCredentialMismatch
	}
	return nil
}

func (s *Services) DeleteAWSCredential(ctx context.Context, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET selected_aws_credential_key = '' WHERE selected_aws_credential_key = ?`, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM aws_credentials WHERE credential_key = ?`, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Services) SelectProjectAWSCredential(ctx context.Context, project, key string) error {
	result, err := s.db.ExecContext(ctx, `
        UPDATE projects p
        INNER JOIN aws_credentials c ON c.credential_key = ? AND c.project_key = p.project_key
        SET p.selected_aws_credential_key = c.credential_key
		WHERE p.project_key = ? AND p.deleted_at IS NULL`, key, project)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return os.ErrNotExist
	}
	return nil
}

func (s *Services) ListHelmComponents(ctx context.Context) ([]componentcatalog.Component, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT component_key, display_name, category, description, repository_url, chart_name,
			chart_version, default_namespace, replica_paths_json, values_yaml, created_by, created_at, updated_at
        FROM helm_component_catalog ORDER BY category, display_name, component_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]componentcatalog.Component, 0)
	for rows.Next() {
		var component componentcatalog.Component
		var replicaPathsJSON []byte
		if err := rows.Scan(&component.Key, &component.DisplayName, &component.Category, &component.Description,
			&component.Repository, &component.Chart, &component.ChartVersion, &component.DefaultNamespace,
			&replicaPathsJSON, &component.ValuesYAML, &component.CreatedBy, &component.CreatedAt, &component.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(replicaPathsJSON, &component.ReplicaPaths)
		result = append(result, component)
	}
	return result, rows.Err()
}

func (s *Services) GetHelmComponent(ctx context.Context, key string) (componentcatalog.Component, error) {
	var component componentcatalog.Component
	var replicaPathsJSON []byte
	err := s.db.QueryRowContext(ctx, `
        SELECT component_key, display_name, category, description, repository_url, chart_name,
			chart_version, default_namespace, replica_paths_json, values_yaml, created_by, created_at, updated_at
        FROM helm_component_catalog WHERE component_key = ?`, key).Scan(
		&component.Key, &component.DisplayName, &component.Category, &component.Description,
		&component.Repository, &component.Chart, &component.ChartVersion, &component.DefaultNamespace,
		&replicaPathsJSON, &component.ValuesYAML, &component.CreatedBy, &component.CreatedAt, &component.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return componentcatalog.Component{}, os.ErrNotExist
	}
	_ = json.Unmarshal(replicaPathsJSON, &component.ReplicaPaths)
	return component, err
}

func (s *Services) SaveHelmComponent(ctx context.Context, component componentcatalog.Component) error {
	replicaPathsJSON, err := json.Marshal(component.ReplicaPaths)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
        INSERT INTO helm_component_catalog (
            component_key, display_name, category, description, repository_url, chart_name,
			chart_version, default_namespace, replica_paths_json, values_yaml, created_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), category = VALUES(category),
            description = VALUES(description), repository_url = VALUES(repository_url),
            chart_name = VALUES(chart_name), chart_version = VALUES(chart_version),
			default_namespace = VALUES(default_namespace), replica_paths_json = VALUES(replica_paths_json), values_yaml = VALUES(values_yaml),
            updated_at = CURRENT_TIMESTAMP(6)`,
		component.Key, component.DisplayName, component.Category, component.Description,
		component.Repository, component.Chart, component.ChartVersion, component.DefaultNamespace,
		replicaPathsJSON, component.ValuesYAML, component.CreatedBy,
	)
	return err
}

func (s *Services) DeleteHelmComponent(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM helm_component_catalog WHERE component_key = ?`, key)
	return err
}

func (s *Services) permissionsForUser(ctx context.Context, username string) ([]access.Permission, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_key, username, can_view, can_deploy, can_configure, can_view_secrets
        FROM project_permissions WHERE username = ? ORDER BY project_key`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]access.Permission, 0)
	for rows.Next() {
		var permission access.Permission
		if err := rows.Scan(&permission.ProjectKey, &permission.Username, &permission.CanView, &permission.CanDeploy, &permission.CanConfigure, &permission.CanViewSecrets); err != nil {
			return nil, err
		}
		result = append(result, permission)
	}
	return result, rows.Err()
}

func (s *Services) EnsureProject(ctx context.Context, project access.Project) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT IGNORE INTO projects (project_key, display_name, description) VALUES (?, ?, ?)`,
		project.Key, project.DisplayName, project.Description)
	return err
}

func (s *Services) ListProjects(ctx context.Context, username string, isAdmin bool) ([]access.Project, error) {
	query := `SELECT p.project_key, p.display_name, p.description, p.selected_aws_credential_key, p.created_at, p.updated_at
		FROM projects p INNER JOIN project_permissions pp ON pp.project_key = p.project_key
		WHERE p.deleted_at IS NULL AND pp.username = ? AND pp.can_view = TRUE ORDER BY p.project_key`
	rows, err := s.db.QueryContext(ctx, query, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]access.Project, 0)
	for rows.Next() {
		var project access.Project
		if err := rows.Scan(&project.Key, &project.DisplayName, &project.Description, &project.SelectedAWSCredentialKey, &project.CreatedAt, &project.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, project)
	}
	return result, rows.Err()
}

// ListProjectsWithDetails avoids the project-list N+1 query pattern. Project,
// current-user permission and up to four environment rows are folded from one
// ordered result set without changing the access-control boundary.
func (s *Services) ListProjectsWithDetails(ctx context.Context, username string, _ bool) ([]access.Project, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			p.project_key, p.display_name, p.description, p.selected_aws_credential_key,
			p.created_at, p.updated_at,
			pp.username, pp.can_view, pp.can_deploy, pp.can_configure, pp.can_view_secrets,
			pe.project_key, pe.environment_key, pe.display_name, pe.target_name, pe.region_name, pe.created_at
		FROM projects p
		INNER JOIN project_permissions pp
			ON pp.project_key = p.project_key AND pp.username = ? AND pp.can_view = TRUE
		LEFT JOIN project_environments pe ON pe.project_key = p.project_key
		WHERE p.deleted_at IS NULL
		ORDER BY p.project_key, FIELD(pe.environment_key, 'dev', 'test', 'uat', 'prod')`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]access.Project, 0)
	projectIndexes := make(map[string]int)
	for rows.Next() {
		var (
			project                   access.Project
			permissionUsername        string
			canView, canDeploy        bool
			canConfigure, viewSecrets bool
			environmentProject        sql.NullString
			environmentKey            sql.NullString
			environmentDisplayName    sql.NullString
			environmentTargetName     sql.NullString
			environmentRegion         sql.NullString
			environmentCreatedAt      sql.NullTime
		)
		if err := rows.Scan(
			&project.Key, &project.DisplayName, &project.Description, &project.SelectedAWSCredentialKey,
			&project.CreatedAt, &project.UpdatedAt,
			&permissionUsername, &canView, &canDeploy, &canConfigure, &viewSecrets,
			&environmentProject, &environmentKey, &environmentDisplayName,
			&environmentTargetName, &environmentRegion, &environmentCreatedAt,
		); err != nil {
			return nil, err
		}
		index, exists := projectIndexes[project.Key]
		if !exists {
			project.Permission = access.Permission{
				ProjectKey: project.Key, Username: permissionUsername,
				CanView: canView, CanDeploy: canDeploy,
				CanConfigure: canConfigure, CanViewSecrets: viewSecrets,
			}
			project.Environments = make([]access.ProjectEnvironment, 0, 4)
			index = len(result)
			projectIndexes[project.Key] = index
			result = append(result, project)
		}
		if environmentKey.Valid {
			result[index].Environments = append(result[index].Environments, access.ProjectEnvironment{
				ProjectKey:  environmentProject.String,
				Environment: environmentKey.String,
				DisplayName: environmentDisplayName.String,
				TargetName:  environmentTargetName.String,
				Region:      environmentRegion.String,
				CreatedAt:   environmentCreatedAt.Time,
			})
		}
	}
	return result, rows.Err()
}

func (s *Services) GetProject(ctx context.Context, key string) (access.Project, error) {
	var project access.Project
	err := s.db.QueryRowContext(ctx, `
		SELECT project_key, display_name, description, selected_aws_credential_key, created_at, updated_at
		FROM projects WHERE project_key = ? AND deleted_at IS NULL`, key).Scan(
		&project.Key, &project.DisplayName, &project.Description, &project.SelectedAWSCredentialKey, &project.CreatedAt, &project.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return access.Project{}, os.ErrNotExist
	}
	return project, err
}

func (s *Services) SaveProject(ctx context.Context, project access.Project) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO projects (project_key, display_name, description) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), description = VALUES(description),
			deleted_at = NULL, updated_at = CURRENT_TIMESTAMP(6)`,
		project.Key, project.DisplayName, project.Description)
	return err
}

func (s *Services) DeleteProject(ctx context.Context, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Keep aws_credentials and the selected credential key. The project row is
	// archived instead of deleted so its encrypted credentials are never
	// removed by the foreign-key cascade and can be recovered by recreating the
	// same stable project key.
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_environments WHERE project_key = ?`, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_permissions WHERE project_key = ?`, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET deleted_at = CURRENT_TIMESTAMP(6), updated_at = CURRENT_TIMESTAMP(6) WHERE project_key = ?`, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Services) ListProjectEnvironments(ctx context.Context, project string) ([]access.ProjectEnvironment, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT project_key, environment_key, display_name, target_name, region_name, created_at
        FROM project_environments WHERE project_key = ?
        ORDER BY FIELD(environment_key, 'dev', 'test', 'uat', 'prod')`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]access.ProjectEnvironment, 0, 4)
	for rows.Next() {
		var item access.ProjectEnvironment
		if err := rows.Scan(&item.ProjectKey, &item.Environment, &item.DisplayName, &item.TargetName, &item.Region, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Services) GetProjectEnvironment(ctx context.Context, project, environment string) (access.ProjectEnvironment, error) {
	var item access.ProjectEnvironment
	err := s.db.QueryRowContext(ctx, `
        SELECT project_key, environment_key, display_name, target_name, region_name, created_at
        FROM project_environments WHERE project_key = ? AND environment_key = ?`, project, environment).Scan(
		&item.ProjectKey, &item.Environment, &item.DisplayName, &item.TargetName, &item.Region, &item.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return access.ProjectEnvironment{}, os.ErrNotExist
	}
	return item, err
}

func (s *Services) SaveProjectEnvironment(ctx context.Context, item access.ProjectEnvironment) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO project_environments (project_key, environment_key, display_name, target_name, region_name)
        VALUES (?, ?, ?, ?, ?)
        ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), target_name = VALUES(target_name),
            region_name = VALUES(region_name), updated_at = CURRENT_TIMESTAMP(6)`,
		item.ProjectKey, item.Environment, item.DisplayName, item.TargetName, item.Region)
	if err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `
        UPDATE deployment_jobs SET project_key = ?, environment_key = ?, target_name = ?
        WHERE (target_name = ? OR (target_name = '' AND environment = ?)) AND project_key = ''`,
		item.ProjectKey, item.Environment, item.TargetName, item.TargetName, item.TargetName)
	return nil
}

func (s *Services) DeleteProjectEnvironment(ctx context.Context, project, environment string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_environments WHERE project_key = ? AND environment_key = ?`, project, environment)
	return err
}

func (s *Services) GetPermission(ctx context.Context, project, username string) (access.Permission, error) {
	var permission access.Permission
	err := s.db.QueryRowContext(ctx, `
		SELECT project_key, username, can_view, can_deploy, can_configure, can_view_secrets
        FROM project_permissions WHERE project_key = ? AND username = ?`, project, username).Scan(
		&permission.ProjectKey, &permission.Username, &permission.CanView, &permission.CanDeploy, &permission.CanConfigure, &permission.CanViewSecrets,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return access.Permission{}, os.ErrNotExist
	}
	return permission, err
}

func (s *Services) SavePermission(ctx context.Context, permission access.Permission) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO project_permissions (project_key, username, can_view, can_deploy, can_configure, can_view_secrets)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE can_view = VALUES(can_view), can_deploy = VALUES(can_deploy), can_configure = VALUES(can_configure),
			can_view_secrets = VALUES(can_view_secrets),
            updated_at = CURRENT_TIMESTAMP(6)`,
		permission.ProjectKey, permission.Username, permission.CanView, permission.CanDeploy, permission.CanConfigure, permission.CanViewSecrets)
	return err
}

func (s *Services) DeletePermission(ctx context.Context, project, username string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_permissions WHERE project_key = ? AND username = ?`, project, username)
	return err
}

func (s *Services) LoadJobs(ctx context.Context) ([]jobs.Job, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, project_key, environment_key, target_name, requested_by, environment,
            action, status, created_at, started_at, finished_at, error_text, log_size, progress_json
        FROM deployment_jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]jobs.Job, 0)
	for rows.Next() {
		var job jobs.Job
		var legacyTarget string
		var started, finished sql.NullTime
		var progressJSON []byte
		if err := rows.Scan(
			&job.ID, &job.Project, &job.Environment, &job.TargetName, &job.RequestedBy, &legacyTarget,
			&job.Action, &job.Status, &job.CreatedAt, &started, &finished, &job.Error, &job.LogSize, &progressJSON,
		); err != nil {
			return nil, err
		}
		if job.TargetName == "" {
			job.TargetName = legacyTarget
		}
		job.StartedAt = nullableTime(started)
		job.FinishedAt = nullableTime(finished)
		if len(progressJSON) > 0 {
			var progress struct {
				Progress         int                   `json:"progress"`
				TotalSteps       int                   `json:"total_steps"`
				SuccessSteps     int                   `json:"success_steps"`
				FailedSteps      int                   `json:"failed_steps"`
				CurrentStep      string                `json:"current_step"`
				Steps            []jobs.Step           `json:"steps"`
				FailureHint      string                `json:"failure_hint"`
				Diagnosis        *jobs.Diagnosis       `json:"diagnosis"`
				IgnoredAt        *time.Time            `json:"ignored_at"`
				IgnoredBy        string                `json:"ignored_by"`
				IgnoreReason     string                `json:"ignore_reason"`
				CompletionAction jobs.CompletionAction `json:"completion_action"`
				Parameters       map[string]string     `json:"parameters"`
			}
			if json.Unmarshal(progressJSON, &progress) == nil {
				job.Progress, job.TotalSteps = progress.Progress, progress.TotalSteps
				job.SuccessSteps, job.FailedSteps = progress.SuccessSteps, progress.FailedSteps
				job.CurrentStep, job.Steps = progress.CurrentStep, progress.Steps
				job.FailureHint = progress.FailureHint
				job.Diagnosis = progress.Diagnosis
				job.IgnoredAt, job.IgnoredBy, job.IgnoreReason = progress.IgnoredAt, progress.IgnoredBy, progress.IgnoreReason
				job.CompletionAction = progress.CompletionAction
				job.Parameters = progress.Parameters
			}
		}
		if job.Steps == nil {
			job.Steps = make([]jobs.Step, 0)
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func (s *Services) SaveJob(ctx context.Context, job *jobs.Job) error {
	progressJSON, err := json.Marshal(map[string]any{
		"progress": job.Progress, "total_steps": job.TotalSteps,
		"success_steps": job.SuccessSteps, "failed_steps": job.FailedSteps,
		"current_step": job.CurrentStep, "steps": job.Steps,
		"failure_hint": job.FailureHint, "diagnosis": job.Diagnosis,
		"ignored_at": job.IgnoredAt, "ignored_by": job.IgnoredBy, "ignore_reason": job.IgnoreReason,
		"completion_action": job.CompletionAction,
		"parameters":        job.Parameters,
	})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
        INSERT INTO deployment_jobs
            (id, environment, project_key, environment_key, target_name, requested_by,
             action, status, created_at, started_at, finished_at, error_text, log_size, progress_json)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON DUPLICATE KEY UPDATE
            status = VALUES(status), started_at = VALUES(started_at), finished_at = VALUES(finished_at),
            error_text = VALUES(error_text), log_size = VALUES(log_size), progress_json = VALUES(progress_json)`,
		job.ID, job.TargetName, job.Project, job.Environment, job.TargetName, job.RequestedBy,
		job.Action, job.Status, job.CreatedAt, job.StartedAt, job.FinishedAt, job.Error, job.LogSize, progressJSON)
	return err
}

func (s *Services) DeleteJob(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployment_jobs WHERE id = ?`, id)
	return err
}

func (s *Services) CacheJob(ctx context.Context, job *jobs.Job) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	pipe := s.redis.TxPipeline()
	pipe.Set(ctx, s.key("job", job.ID), payload, s.jobStateTTL)
	pipe.Set(ctx, s.key("project", job.Project, "environment", job.Environment, "current-job"), payload, s.jobStateTTL)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *Services) DeleteCachedJob(ctx context.Context, id, project, environment string) error {
	return s.redis.Del(ctx, s.key("job", id), s.key("project", project, "environment", environment, "current-job")).Err()
}

func (s *Services) GetStatus(ctx context.Context, environment string) ([]byte, bool, error) {
	payload, err := s.redis.Get(ctx, s.key("status", environment)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	return payload, err == nil, err
}

func (s *Services) GetStatuses(ctx context.Context, environments []string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	if len(environments) == 0 {
		return result, nil
	}
	keys := make([]string, len(environments))
	for index, environment := range environments {
		keys[index] = s.key("status", environment)
	}
	values, err := s.redis.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for index, value := range values {
		var payload []byte
		switch typed := value.(type) {
		case string:
			payload = []byte(typed)
		case []byte:
			payload = append([]byte(nil), typed...)
		default:
			continue
		}
		result[environments[index]] = payload
	}
	return result, nil
}

func (s *Services) SetStatus(ctx context.Context, environment string, payload []byte) error {
	return s.redis.Set(ctx, s.key("status", environment), payload, s.statusTTL).Err()
}

func (s *Services) DeleteStatus(ctx context.Context, environment string) error {
	return s.redis.Del(ctx, s.key("status", environment)).Err()
}

func (s *Services) LoadResourceSnapshot(ctx context.Context, project, environment string) ([]byte, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT snapshot_json FROM environment_resource_snapshots
		WHERE project_key = ? AND environment_key = ?`, project, environment).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, os.ErrNotExist
	}
	return payload, err
}

func (s *Services) SaveResourceSnapshot(ctx context.Context, project, environment string, payload []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO environment_resource_snapshots (project_key, environment_key, snapshot_json)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE snapshot_json = VALUES(snapshot_json), updated_at = CURRENT_TIMESTAMP(6)`,
		project, environment, payload)
	return err
}

func (s *Services) ListStaticCDNs(ctx context.Context, project, environment string) ([]staticcdn.Resource, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_key, environment_key, display_name, bucket_name, region_name,
			cors_origins_json, distribution_id, distribution_arn, domain_name, oac_id,
			status_name, last_error, created_by, created_at, updated_at
		FROM static_cdn_resources
		WHERE project_key = ? AND environment_key = ?
		ORDER BY created_at, bucket_name`, project, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]staticcdn.Resource, 0)
	for rows.Next() {
		var item staticcdn.Resource
		var corsJSON []byte
		if err := rows.Scan(
			&item.ProjectKey, &item.EnvironmentKey, &item.DisplayName, &item.BucketName, &item.Region,
			&corsJSON, &item.DistributionID, &item.DistributionARN, &item.DomainName, &item.OACID,
			&item.Status, &item.LastError, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(corsJSON, &item.CORSOrigins); err != nil {
			return nil, err
		}
		if item.DomainName != "" {
			item.CDNURL = "https://" + item.DomainName
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Services) GetStaticCDN(ctx context.Context, project, environment, bucket string) (staticcdn.Resource, error) {
	var item staticcdn.Resource
	var corsJSON []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT project_key, environment_key, display_name, bucket_name, region_name,
			cors_origins_json, distribution_id, distribution_arn, domain_name, oac_id,
			status_name, last_error, created_by, created_at, updated_at
		FROM static_cdn_resources
		WHERE project_key = ? AND environment_key = ? AND bucket_name = ?`,
		project, environment, bucket).Scan(
		&item.ProjectKey, &item.EnvironmentKey, &item.DisplayName, &item.BucketName, &item.Region,
		&corsJSON, &item.DistributionID, &item.DistributionARN, &item.DomainName, &item.OACID,
		&item.Status, &item.LastError, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return staticcdn.Resource{}, os.ErrNotExist
	}
	if err != nil {
		return staticcdn.Resource{}, err
	}
	if err := json.Unmarshal(corsJSON, &item.CORSOrigins); err != nil {
		return staticcdn.Resource{}, err
	}
	if item.DomainName != "" {
		item.CDNURL = "https://" + item.DomainName
	}
	return item, nil
}

func (s *Services) SaveStaticCDN(ctx context.Context, item staticcdn.Resource) error {
	corsJSON, err := json.Marshal(item.CORSOrigins)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO static_cdn_resources (
			project_key, environment_key, bucket_name, display_name, region_name,
			cors_origins_json, distribution_id, distribution_arn, domain_name, oac_id,
			status_name, last_error, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), region_name = VALUES(region_name),
			cors_origins_json = VALUES(cors_origins_json), distribution_id = VALUES(distribution_id),
			distribution_arn = VALUES(distribution_arn), domain_name = VALUES(domain_name),
			oac_id = VALUES(oac_id), status_name = VALUES(status_name), last_error = VALUES(last_error),
			updated_at = CURRENT_TIMESTAMP(6)`,
		item.ProjectKey, item.EnvironmentKey, item.BucketName, item.DisplayName, item.Region,
		corsJSON, item.DistributionID, item.DistributionARN, item.DomainName, item.OACID,
		item.Status, item.LastError, item.CreatedBy, item.CreatedAt,
	)
	return err
}

func (s *Services) DeleteStaticCDN(ctx context.Context, project, environment, bucket string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM static_cdn_resources
		WHERE project_key = ? AND environment_key = ? AND bucket_name = ?`, project, environment, bucket)
	if err != nil {
		return err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected == 0 {
		return os.ErrNotExist
	}
	return nil
}

func (s *Services) RecordAudit(ctx context.Context, method, path string, status int, username, remote string, duration time.Duration) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO audit_events (occurred_at, username, method, path, response_status, remote_address, duration_ms)
        VALUES (?, ?, ?, ?, ?, ?, ?)`, time.Now().UTC(), username, method, path, status, remote, duration.Milliseconds())
	return err
}

func (s *Services) ListAuditEvents(ctx context.Context, query auditlog.Query) (auditlog.Page, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}

	conditions := []string{"1 = 1"}
	args := make([]any, 0, 8)
	if !query.IncludeSystem {
		conditions = append(conditions, "path NOT LIKE '/api/internal/%' AND path NOT LIKE '/api/cicd/webhooks/%'")
	}
	if query.Username != "" {
		conditions = append(conditions, "username = ?")
		args = append(args, query.Username)
	}
	if query.Method != "" {
		conditions = append(conditions, "method = ?")
		args = append(args, strings.ToUpper(query.Method))
	}
	switch query.Result {
	case "success":
		conditions = append(conditions, "response_status >= 200 AND response_status < 400")
	case "failed":
		conditions = append(conditions, "response_status >= 400")
	}
	if query.Keyword != "" {
		conditions = append(conditions, "(username LIKE ? OR path LIKE ?)")
		value := "%" + query.Keyword + "%"
		args = append(args, value, value)
	}
	if query.From != nil {
		conditions = append(conditions, "occurred_at >= ?")
		args = append(args, query.From.UTC())
	}
	if query.To != nil {
		conditions = append(conditions, "occurred_at <= ?")
		args = append(args, query.To.UTC())
	}
	where := strings.Join(conditions, " AND ")

	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE "+where, args...).Scan(&total); err != nil {
		return auditlog.Page{}, fmt.Errorf("count audit events: %w", err)
	}

	offset := (query.Page - 1) * query.PageSize
	listArgs := append(append([]any(nil), args...), query.PageSize, offset)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, occurred_at, username, method, path, response_status, remote_address, duration_ms
		FROM audit_events
		WHERE `+where+`
		ORDER BY occurred_at DESC, id DESC
		LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return auditlog.Page{}, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	items := make([]auditlog.Event, 0, query.PageSize)
	for rows.Next() {
		var event auditlog.Event
		if err := rows.Scan(
			&event.ID, &event.OccurredAt, &event.Username, &event.Method, &event.Path,
			&event.ResponseStatus, &event.RemoteAddress, &event.DurationMS,
		); err != nil {
			return auditlog.Page{}, fmt.Errorf("scan audit event: %w", err)
		}
		items = append(items, auditlog.Enrich(event))
	}
	if err := rows.Err(); err != nil {
		return auditlog.Page{}, fmt.Errorf("iterate audit events: %w", err)
	}
	return auditlog.Page{Items: items, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *Services) Health(ctx context.Context) (map[string]string, error) {
	result := map[string]string{"mysql": "down", "redis": "down"}
	type dependencyResult struct {
		name string
		err  error
	}
	checks := make(chan dependencyResult, 2)
	go func() {
		checks <- dependencyResult{name: "mysql", err: s.db.PingContext(ctx)}
	}()
	go func() {
		checks <- dependencyResult{name: "redis", err: s.redis.Ping(ctx).Err()}
	}()
	var healthErr error
	for range 2 {
		check := <-checks
		if check.err != nil {
			healthErr = errors.Join(healthErr, fmt.Errorf("%s: %w", check.name, check.err))
			continue
		}
		result[check.name] = "up"
	}
	return result, healthErr
}

func (s *Services) key(parts ...string) string {
	return s.keyPrefix + ":" + strings.Join(parts, ":")
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}
