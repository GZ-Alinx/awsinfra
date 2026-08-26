package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/GZ-Alinx/awsinfra/internal/gitlab"
)

func (s *Services) ListGitLabServers(ctx context.Context) ([]gitlab.StoredServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_key,display_name,base_url,root_group,root_groups_json,default_branch,visibility_name,allow_insecure_http,token_cipher,last_check_status,last_check_error,last_checked_at,created_at,updated_at FROM gitlab_servers ORDER BY display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []gitlab.StoredServer{}
	for rows.Next() {
		var item gitlab.StoredServer
		var checked sql.NullTime
		var rootGroups []byte
		if err := rows.Scan(&item.Key, &item.DisplayName, &item.BaseURL, &item.RootGroup, &rootGroups, &item.DefaultBranch, &item.Visibility, &item.AllowInsecureHTTP, &item.TokenCipher, &item.LastCheckStatus, &item.LastCheckError, &checked, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.RootGroups = decodeGitLabRootGroups(rootGroups, item.RootGroup)
		if checked.Valid {
			item.LastCheckedAt = checked.Time
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Services) GetGitLabServer(ctx context.Context, key string) (gitlab.StoredServer, error) {
	var item gitlab.StoredServer
	var checked sql.NullTime
	var rootGroups []byte
	err := s.db.QueryRowContext(ctx, `SELECT server_key,display_name,base_url,root_group,root_groups_json,default_branch,visibility_name,allow_insecure_http,token_cipher,last_check_status,last_check_error,last_checked_at,created_at,updated_at FROM gitlab_servers WHERE server_key=?`, key).
		Scan(&item.Key, &item.DisplayName, &item.BaseURL, &item.RootGroup, &rootGroups, &item.DefaultBranch, &item.Visibility, &item.AllowInsecureHTTP, &item.TokenCipher, &item.LastCheckStatus, &item.LastCheckError, &checked, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, os.ErrNotExist
	}
	if checked.Valid {
		item.LastCheckedAt = checked.Time
	}
	item.RootGroups = decodeGitLabRootGroups(rootGroups, item.RootGroup)
	return item, err
}

func (s *Services) SaveGitLabServer(ctx context.Context, item gitlab.StoredServer) error {
	rootGroups, err := json.Marshal(item.RootGroups)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO gitlab_servers(server_key,display_name,base_url,root_group,root_groups_json,default_branch,visibility_name,allow_insecure_http,token_cipher,last_check_status,last_check_error,last_checked_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE display_name=VALUES(display_name),base_url=VALUES(base_url),root_group=VALUES(root_group),root_groups_json=VALUES(root_groups_json),default_branch=VALUES(default_branch),visibility_name=VALUES(visibility_name),allow_insecure_http=VALUES(allow_insecure_http),token_cipher=VALUES(token_cipher),last_check_status=VALUES(last_check_status),last_check_error=VALUES(last_check_error),last_checked_at=VALUES(last_checked_at),updated_at=CURRENT_TIMESTAMP(6)`,
		item.Key, item.DisplayName, item.BaseURL, item.RootGroup, rootGroups, item.DefaultBranch, item.Visibility, item.AllowInsecureHTTP, item.TokenCipher, item.LastCheckStatus, item.LastCheckError, nullableGitLabTime(item.LastCheckedAt))
	return err
}

func (s *Services) DeleteGitLabServer(ctx context.Context, key string) error {
	count, err := s.GitLabServerBindingCount(ctx, key)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%w：该 GitLab 服务器仍被 %d 个项目绑定，请先在对应项目中解除仓库绑定", gitlab.ErrConflict, count)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM gitlab_servers WHERE server_key=?`, key)
	if err == nil {
		if affected, _ := result.RowsAffected(); affected == 0 {
			return os.ErrNotExist
		}
	}
	return err
}

func (s *Services) GitLabServerBindingCount(ctx context.Context, key string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_gitlab_delivery WHERE server_key=? OR source_server_key=?`, key, key).Scan(&count)
	return count, err
}

func (s *Services) GitLabServerBindingRootGroups(ctx context.Context, key string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT root_group FROM project_gitlab_delivery WHERE server_key=?
		UNION
		SELECT source_root_group FROM project_gitlab_delivery WHERE source_server_key=? AND source_root_group<>''`, key, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var rootGroup string
		if err := rows.Scan(&rootGroup); err != nil {
			return nil, err
		}
		if rootGroup != "" {
			result = append(result, rootGroup)
		}
	}
	return result, rows.Err()
}

func (s *Services) GitLabServerSourceRepositories(ctx context.Context, key string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT services_json FROM project_gitlab_delivery WHERE source_server_key=?`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var services []gitlab.ServiceSpec
		if err := json.Unmarshal(payload, &services); err != nil {
			return nil, err
		}
		for _, service := range services {
			repository := service.SourceRepository
			if repository != "" && !seen[repository] {
				seen[repository] = true
				result = append(result, repository)
			}
		}
	}
	return result, rows.Err()
}

func decodeGitLabRootGroups(payload []byte, fallback string) []string {
	result := []string{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &result)
	}
	if len(result) == 0 && fallback != "" {
		result = []string{fallback}
	}
	return result
}

func (s *Services) GetProjectGitLabDelivery(ctx context.Context, project string) (gitlab.ProjectDelivery, error) {
	var item gitlab.ProjectDelivery
	var services []byte
	var provisioned sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT project_key,COALESCE(server_key,''),root_group,source_server_key,source_root_group,default_branch,jenkinsfiles_project_id,jenkinsfiles_project_path,jenkinsfiles_clone_url,manifests_project_id,manifests_project_path,manifests_clone_url,services_json,provision_status,provision_error,last_provisioned_at,created_at,updated_at FROM project_gitlab_delivery WHERE project_key=?`, project).
		Scan(&item.ProjectKey, &item.ServerKey, &item.RootGroup, &item.SourceServerKey, &item.SourceRootGroup, &item.DefaultBranch, &item.JenkinsfilesProjectID, &item.JenkinsfilesProjectPath, &item.JenkinsfilesCloneURL, &item.ManifestsProjectID, &item.ManifestsProjectPath, &item.ManifestsCloneURL, &services, &item.ProvisionStatus, &item.ProvisionError, &provisioned, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, os.ErrNotExist
	}
	if err != nil {
		return item, err
	}
	if err := json.Unmarshal(services, &item.Services); err != nil {
		return item, err
	}
	if item.Services == nil {
		item.Services = []gitlab.ServiceSpec{}
	}
	if provisioned.Valid {
		item.LastProvisionedAt = provisioned.Time
	}
	return item, nil
}

func (s *Services) SaveProjectGitLabDelivery(ctx context.Context, item gitlab.ProjectDelivery) error {
	services, err := json.Marshal(item.Services)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO project_gitlab_delivery(project_key,server_key,root_group,source_server_key,source_root_group,default_branch,jenkinsfiles_project_id,jenkinsfiles_project_path,jenkinsfiles_clone_url,manifests_project_id,manifests_project_path,manifests_clone_url,services_json,provision_status,provision_error,last_provisioned_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE server_key=VALUES(server_key),root_group=VALUES(root_group),source_server_key=VALUES(source_server_key),source_root_group=VALUES(source_root_group),default_branch=VALUES(default_branch),jenkinsfiles_project_id=VALUES(jenkinsfiles_project_id),jenkinsfiles_project_path=VALUES(jenkinsfiles_project_path),jenkinsfiles_clone_url=VALUES(jenkinsfiles_clone_url),manifests_project_id=VALUES(manifests_project_id),manifests_project_path=VALUES(manifests_project_path),manifests_clone_url=VALUES(manifests_clone_url),services_json=VALUES(services_json),provision_status=VALUES(provision_status),provision_error=VALUES(provision_error),last_provisioned_at=VALUES(last_provisioned_at),updated_at=CURRENT_TIMESTAMP(6)`,
		item.ProjectKey, nullableGitLabServerKey(item.ServerKey), item.RootGroup, item.SourceServerKey, item.SourceRootGroup, item.DefaultBranch, item.JenkinsfilesProjectID, item.JenkinsfilesProjectPath, item.JenkinsfilesCloneURL, item.ManifestsProjectID, item.ManifestsProjectPath, item.ManifestsCloneURL, services, item.ProvisionStatus, item.ProvisionError, nullableGitLabTime(item.LastProvisionedAt))
	return err
}

func (s *Services) DetachProjectGitLabDelivery(ctx context.Context, project string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE project_gitlab_delivery
		SET server_key=NULL,
			root_group='',
			source_server_key='',
			source_root_group='',
			jenkinsfiles_project_id=0,
			jenkinsfiles_project_path='',
			jenkinsfiles_clone_url='',
			manifests_project_id=0,
			manifests_project_path='',
			manifests_clone_url='',
			provision_status='',
			provision_error='',
			last_provisioned_at=NULL,
			updated_at=CURRENT_TIMESTAMP(6)
		WHERE project_key=?`, project)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return os.ErrNotExist
	}
	return nil
}

func nullableGitLabTime(value interface{ IsZero() bool }) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func nullableGitLabServerKey(value string) any {
	if value == "" {
		return nil
	}
	return value
}
