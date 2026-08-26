package access

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type EnvironmentDefinition struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Order       int    `json:"order"`
}

var EnvironmentDefinitions = []EnvironmentDefinition{
	{Key: "dev", DisplayName: "开发环境", Order: 1},
	{Key: "test", DisplayName: "测试环境", Order: 2},
	{Key: "uat", DisplayName: "预发布环境", Order: 3},
	{Key: "prod", DisplayName: "生产环境", Order: 4},
}

type Project struct {
	Key                      string               `json:"key"`
	DisplayName              string               `json:"display_name"`
	Description              string               `json:"description"`
	SelectedAWSCredentialKey string               `json:"selected_aws_credential_key"`
	Environments             []ProjectEnvironment `json:"environments"`
	Permission               Permission           `json:"permission"`
	CreatedAt                time.Time            `json:"created_at"`
	UpdatedAt                time.Time            `json:"updated_at"`
}

type ProjectEnvironment struct {
	ProjectKey         string     `json:"project_key"`
	Environment        string     `json:"environment"`
	DisplayName        string     `json:"display_name"`
	TargetName         string     `json:"target_name"`
	Region             string     `json:"region"`
	LifecycleStatus    string     `json:"lifecycle_status,omitempty"`
	LifecycleDetail    string     `json:"lifecycle_detail,omitempty"`
	LifecycleUpdatedAt *time.Time `json:"lifecycle_updated_at,omitempty"`
	LatestJobID        string     `json:"latest_job_id,omitempty"`
	LatestJobAction    string     `json:"latest_job_action,omitempty"`
	LatestJobStatus    string     `json:"latest_job_status,omitempty"`
	LatestJobProgress  int        `json:"latest_job_progress,omitempty"`
	PhaseOneDeployed   bool       `json:"phase_one_deployed"`
	PhaseTwoDeployed   bool       `json:"phase_two_deployed"`
	CreatedAt          time.Time  `json:"created_at"`
}

type Permission struct {
	ProjectKey     string `json:"project_key"`
	Username       string `json:"username,omitempty"`
	CanView        bool   `json:"can_view"`
	CanDeploy      bool   `json:"can_deploy"`
	CanConfigure   bool   `json:"can_configure"`
	CanViewSecrets bool   `json:"can_view_secrets"`
}

type PlatformPermission struct {
	CanManageProjects    bool `json:"can_manage_projects"`
	CanManageUsers       bool `json:"can_manage_users"`
	CanManageCredentials bool `json:"can_manage_credentials"`
	CanManageComponents  bool `json:"can_manage_components"`
	CanViewAudit         bool `json:"can_view_audit"`
}

func FullPlatformPermission() PlatformPermission {
	return PlatformPermission{
		CanManageProjects: true, CanManageUsers: true,
		CanManageCredentials: true, CanManageComponents: true, CanViewAudit: true,
	}
}

func (permission PlatformPermission) Any() bool {
	return permission.CanManageProjects || permission.CanManageUsers ||
		permission.CanManageCredentials || permission.CanManageComponents || permission.CanViewAudit
}

type User struct {
	Username            string             `json:"username"`
	DisplayName         string             `json:"display_name"`
	PasswordHash        string             `json:"-"`
	IsAdmin             bool               `json:"is_admin"`
	Active              bool               `json:"active"`
	PlatformPermissions PlatformPermission `json:"platform_permissions"`
	Permissions         []Permission       `json:"permissions"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

type BootstrapEnvironment struct {
	ProjectKey  string
	Environment string
	TargetName  string
	Region      string
}

type Store interface {
	EnsureAdmin(context.Context, User) error
	GetUser(context.Context, string) (User, error)
	ListUsers(context.Context) ([]User, error)
	SaveUser(context.Context, User) error
	DeleteUser(context.Context, string) error

	EnsureProject(context.Context, Project) error
	ListProjects(context.Context, string, bool) ([]Project, error)
	GetProject(context.Context, string) (Project, error)
	SaveProject(context.Context, Project) error
	DeleteProject(context.Context, string) error

	ListProjectEnvironments(context.Context, string) ([]ProjectEnvironment, error)
	GetProjectEnvironment(context.Context, string, string) (ProjectEnvironment, error)
	SaveProjectEnvironment(context.Context, ProjectEnvironment) error
	DeleteProjectEnvironment(context.Context, string, string) error

	GetPermission(context.Context, string, string) (Permission, error)
	SavePermission(context.Context, Permission) error
	DeletePermission(context.Context, string, string) error
}

// detailedProjectStore is an optional fast path for database-backed stores.
// It returns the same project model as the regular Store methods, but hydrates
// project permissions and environments in one query. File-backed and test
// stores can keep using the small, method-by-method fallback below.
type detailedProjectStore interface {
	ListProjectsWithDetails(context.Context, string, bool) ([]Project, error)
}

type Service struct {
	store Store
}

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) Bootstrap(ctx context.Context, adminUsername, adminHash string, environments []BootstrapEnvironment) error {
	if err := s.store.EnsureAdmin(ctx, User{
		Username: adminUsername, DisplayName: "系统管理员", PasswordHash: adminHash, IsAdmin: true, Active: true,
		PlatformPermissions: FullPlatformPermission(),
	}); err != nil {
		return fmt.Errorf("bootstrap administrator: %w", err)
	}
	for _, item := range environments {
		if !ValidProjectKey(item.ProjectKey) || !ValidEnvironment(item.Environment) {
			continue
		}
		project := Project{Key: item.ProjectKey, DisplayName: item.ProjectKey, Description: "从已有环境配置自动导入"}
		if err := s.store.EnsureProject(ctx, project); err != nil {
			return fmt.Errorf("bootstrap project %s: %w", item.ProjectKey, err)
		}
		if err := s.store.SavePermission(ctx, Permission{
			ProjectKey: item.ProjectKey, Username: adminUsername, CanView: true,
			CanDeploy: true, CanConfigure: true, CanViewSecrets: true,
		}); err != nil {
			return fmt.Errorf("bootstrap project permission %s: %w", item.ProjectKey, err)
		}
		mapping := ProjectEnvironment{
			ProjectKey: item.ProjectKey, Environment: item.Environment,
			DisplayName: EnvironmentName(item.Environment), TargetName: item.TargetName, Region: item.Region,
		}
		if err := s.store.SaveProjectEnvironment(ctx, mapping); err != nil {
			return fmt.Errorf("bootstrap project environment %s/%s: %w", item.ProjectKey, item.Environment, err)
		}
	}
	return nil
}

func (s *Service) GetUser(ctx context.Context, username string) (User, error) {
	return s.store.GetUser(ctx, username)
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	return s.store.ListUsers(ctx)
}

func (s *Service) SaveUser(ctx context.Context, user User) error {
	user.Username = strings.TrimSpace(strings.ToLower(user.Username))
	user.DisplayName = strings.TrimSpace(user.DisplayName)
	if !ValidUsername(user.Username) {
		return ErrInvalidUsername
	}
	if user.DisplayName == "" {
		user.DisplayName = user.Username
	}
	return s.store.SaveUser(ctx, user)
}

func (s *Service) DeleteUser(ctx context.Context, username string) error {
	return s.store.DeleteUser(ctx, username)
}

func (s *Service) ListProjects(ctx context.Context, username string, isAdmin bool) ([]Project, error) {
	if store, ok := s.store.(detailedProjectStore); ok {
		projects, err := store.ListProjectsWithDetails(ctx, username, isAdmin)
		if err != nil {
			return nil, err
		}
		sort.Slice(projects, func(i, j int) bool { return projects[i].Key < projects[j].Key })
		return projects, nil
	}
	projects, err := s.store.ListProjects(ctx, username, false)
	if err != nil {
		return nil, err
	}
	for index := range projects {
		environments, envErr := s.store.ListProjectEnvironments(ctx, projects[index].Key)
		if envErr != nil {
			return nil, envErr
		}
		projects[index].Environments = environments
		projects[index].Permission, _ = s.store.GetPermission(ctx, projects[index].Key, username)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Key < projects[j].Key })
	return projects, nil
}

func (s *Service) GetProject(ctx context.Context, key string) (Project, error) {
	project, err := s.store.GetProject(ctx, key)
	if err != nil {
		return Project{}, err
	}
	project.Environments, err = s.store.ListProjectEnvironments(ctx, key)
	return project, err
}

func (s *Service) SaveProject(ctx context.Context, project Project) error {
	project.Key = NormalizeProjectKey(project.Key)
	project.DisplayName = strings.TrimSpace(project.DisplayName)
	project.Description = strings.TrimSpace(project.Description)
	if !ValidProjectKey(project.Key) {
		return ErrInvalidProject
	}
	if length := utf8.RuneCountInString(project.DisplayName); length < 1 || length > 128 {
		return ErrInvalidProjectDisplayName
	}
	if utf8.RuneCountInString(project.Description) > 1000 {
		return ErrProjectDescriptionTooLong
	}
	return s.store.SaveProject(ctx, project)
}

func (s *Service) DeleteProject(ctx context.Context, key string) error {
	return s.store.DeleteProject(ctx, key)
}

func (s *Service) Environment(ctx context.Context, project, environment string) (ProjectEnvironment, error) {
	if !ValidProjectKey(project) || !ValidEnvironment(environment) {
		return ProjectEnvironment{}, os.ErrNotExist
	}
	return s.store.GetProjectEnvironment(ctx, project, environment)
}

func (s *Service) SaveEnvironment(ctx context.Context, item ProjectEnvironment) error {
	if !ValidProjectKey(item.ProjectKey) || !ValidEnvironment(item.Environment) || strings.TrimSpace(item.TargetName) == "" {
		return ErrInvalidEnvironment
	}
	item.DisplayName = EnvironmentName(item.Environment)
	return s.store.SaveProjectEnvironment(ctx, item)
}

func (s *Service) DeleteEnvironment(ctx context.Context, project, environment string) error {
	return s.store.DeleteProjectEnvironment(ctx, project, environment)
}

func (s *Service) Permission(ctx context.Context, username string, isAdmin bool, project string) (Permission, error) {
	return s.store.GetPermission(ctx, project, username)
}

func (s *Service) RequireView(ctx context.Context, username string, isAdmin bool, project string) (Permission, error) {
	permission, err := s.Permission(ctx, username, isAdmin, project)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Permission{}, ErrForbidden
		}
		return Permission{}, err
	}
	if !permission.CanView {
		return Permission{}, ErrForbidden
	}
	return permission, nil
}

func (s *Service) RequireDeploy(ctx context.Context, username string, isAdmin bool, project string) error {
	permission, err := s.RequireView(ctx, username, isAdmin, project)
	if err != nil {
		return err
	}
	if !permission.CanDeploy {
		return ErrForbidden
	}
	return nil
}

func (s *Service) RequireConfigure(ctx context.Context, username string, isAdmin bool, project string) error {
	permission, err := s.RequireView(ctx, username, isAdmin, project)
	if err != nil {
		return err
	}
	if !permission.CanConfigure {
		return ErrForbidden
	}
	return nil
}

func (s *Service) RequireViewSecrets(ctx context.Context, username string, isAdmin bool, project string) error {
	permission, err := s.RequireView(ctx, username, isAdmin, project)
	if err != nil {
		return err
	}
	if !permission.CanViewSecrets {
		return ErrForbidden
	}
	return nil
}

func (s *Service) SavePermission(ctx context.Context, permission Permission) error {
	if !ValidProjectKey(permission.ProjectKey) || !ValidUsername(permission.Username) {
		return ErrInvalidPermission
	}
	if permission.CanDeploy || permission.CanConfigure || permission.CanViewSecrets {
		permission.CanView = true
	}
	if !permission.CanView {
		return s.store.DeletePermission(ctx, permission.ProjectKey, permission.Username)
	}
	return s.store.SavePermission(ctx, permission)
}

func (s *Service) DeletePermission(ctx context.Context, project, username string) error {
	return s.store.DeletePermission(ctx, project, username)
}

func EnvironmentName(key string) string {
	for _, definition := range EnvironmentDefinitions {
		if definition.Key == key {
			return definition.DisplayName
		}
	}
	return key
}

func ValidEnvironment(value string) bool {
	for _, definition := range EnvironmentDefinitions {
		if definition.Key == value {
			return true
		}
	}
	return false
}

var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{2,63}$`)

func ValidProjectKey(value string) bool { return projectPattern.MatchString(value) }

// NormalizeProjectKey converts a user-facing project label into the restricted
// identifier required by Kubernetes, Terraform and AWS resource names. Chinese
// and other non-ASCII characters remain available in Project.DisplayName.
func NormalizeProjectKey(value string) string {
	raw := strings.TrimSpace(strings.ToLower(value))
	if raw == "" {
		return ""
	}
	var builder strings.Builder
	separator := false
	for _, character := range raw {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			if separator && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(character)
			separator = false
		default:
			separator = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(raw))
		slug = fmt.Sprintf("project-%08x", hash.Sum32())
	}
	if slug[0] >= '0' && slug[0] <= '9' {
		slug = "p-" + slug
	}
	if len(slug) > 48 {
		slug = strings.TrimRight(slug[:48], "-")
	}
	return slug
}
func ValidUsername(value string) bool { return usernamePattern.MatchString(value) }

var (
	ErrForbidden                 = errors.New("project permission denied")
	ErrInvalidProject            = errors.New("invalid project key")
	ErrInvalidProjectDisplayName = errors.New("project display name must contain 1 to 128 characters")
	ErrProjectDescriptionTooLong = errors.New("project description must not exceed 1000 characters")
	ErrInvalidEnvironment        = errors.New("environment must be one of dev, test, uat, or prod")
	ErrInvalidUsername           = errors.New("invalid username")
	ErrInvalidPermission         = errors.New("invalid project permission")
)
