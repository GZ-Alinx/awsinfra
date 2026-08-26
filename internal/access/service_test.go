package access

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type accessStoreStub struct {
	projects    map[string]Project
	permissions map[string]Permission
}

type detailedAccessStoreStub struct {
	*accessStoreStub
	projectsWithDetails []Project
	detailedCalls       int
}

func (s *detailedAccessStoreStub) ListProjectsWithDetails(context.Context, string, bool) ([]Project, error) {
	s.detailedCalls++
	return append([]Project(nil), s.projectsWithDetails...), nil
}

func permissionKey(project, username string) string { return project + "\x00" + username }

func (s *accessStoreStub) EnsureAdmin(context.Context, User) error { return nil }
func (s *accessStoreStub) GetUser(context.Context, string) (User, error) {
	return User{}, os.ErrNotExist
}
func (s *accessStoreStub) ListUsers(context.Context) ([]User, error) { return nil, nil }
func (s *accessStoreStub) SaveUser(context.Context, User) error      { return nil }
func (s *accessStoreStub) DeleteUser(context.Context, string) error  { return nil }
func (s *accessStoreStub) EnsureProject(_ context.Context, project Project) error {
	s.projects[project.Key] = project
	return nil
}
func (s *accessStoreStub) ListProjects(_ context.Context, username string, isAdmin bool) ([]Project, error) {
	result := make([]Project, 0)
	for key, project := range s.projects {
		if isAdmin {
			result = append(result, project)
			continue
		}
		if permission, ok := s.permissions[permissionKey(key, username)]; ok && permission.CanView {
			result = append(result, project)
		}
	}
	return result, nil
}
func (s *accessStoreStub) GetProject(_ context.Context, key string) (Project, error) {
	project, ok := s.projects[key]
	if !ok {
		return Project{}, os.ErrNotExist
	}
	return project, nil
}
func (s *accessStoreStub) SaveProject(_ context.Context, project Project) error {
	s.projects[project.Key] = project
	return nil
}
func (s *accessStoreStub) DeleteProject(_ context.Context, key string) error {
	delete(s.projects, key)
	return nil
}
func (s *accessStoreStub) ListProjectEnvironments(context.Context, string) ([]ProjectEnvironment, error) {
	return nil, nil
}
func (s *accessStoreStub) GetProjectEnvironment(context.Context, string, string) (ProjectEnvironment, error) {
	return ProjectEnvironment{}, os.ErrNotExist
}
func (s *accessStoreStub) SaveProjectEnvironment(context.Context, ProjectEnvironment) error {
	return nil
}
func (s *accessStoreStub) DeleteProjectEnvironment(context.Context, string, string) error { return nil }
func (s *accessStoreStub) GetPermission(_ context.Context, project, username string) (Permission, error) {
	permission, ok := s.permissions[permissionKey(project, username)]
	if !ok {
		return Permission{}, os.ErrNotExist
	}
	return permission, nil
}
func (s *accessStoreStub) SavePermission(_ context.Context, permission Permission) error {
	s.permissions[permissionKey(permission.ProjectKey, permission.Username)] = permission
	return nil
}
func (s *accessStoreStub) DeletePermission(_ context.Context, project, username string) error {
	delete(s.permissions, permissionKey(project, username))
	return nil
}

func TestAdministratorStillRequiresProjectMembership(t *testing.T) {
	store := &accessStoreStub{
		projects: map[string]Project{"alpha": {Key: "alpha"}, "beta": {Key: "beta"}},
		permissions: map[string]Permission{
			permissionKey("alpha", "admin"): {ProjectKey: "alpha", Username: "admin", CanView: true, CanConfigure: true},
		},
	}
	service := NewService(store)
	projects, err := service.ListProjects(context.Background(), "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Key != "alpha" {
		t.Fatalf("administrator project access bypassed membership: %#v", projects)
	}
	if _, err := service.RequireView(context.Background(), "admin", true, "beta"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden for unassigned project, got %v", err)
	}
}

func TestListProjectsUsesDetailedStoreFastPath(t *testing.T) {
	store := &detailedAccessStoreStub{
		accessStoreStub: &accessStoreStub{
			projects:    map[string]Project{},
			permissions: map[string]Permission{},
		},
		projectsWithDetails: []Project{{
			Key:        "beta",
			Permission: Permission{ProjectKey: "beta", Username: "operator", CanView: true},
			Environments: []ProjectEnvironment{{
				ProjectKey: "beta", Environment: "test", TargetName: "beta-test",
			}},
		}, {
			Key:          "alpha",
			Permission:   Permission{ProjectKey: "alpha", Username: "operator", CanView: true},
			Environments: make([]ProjectEnvironment, 0),
		}},
	}

	projects, err := NewService(store).ListProjects(context.Background(), "operator", false)
	if err != nil {
		t.Fatal(err)
	}
	if store.detailedCalls != 1 {
		t.Fatalf("detailed project loader calls = %d, want 1", store.detailedCalls)
	}
	if len(projects) != 2 || projects[0].Key != "alpha" || projects[1].Key != "beta" {
		t.Fatalf("detailed projects were not returned in stable order: %#v", projects)
	}
	if len(projects[1].Environments) != 1 || projects[1].Environments[0].TargetName != "beta-test" {
		t.Fatalf("hydrated environments were lost: %#v", projects[1].Environments)
	}
}

func TestViewOnlyPermissionIsPersisted(t *testing.T) {
	store := &accessStoreStub{projects: map[string]Project{"alpha": {Key: "alpha"}}, permissions: make(map[string]Permission)}
	service := NewService(store)
	permission := Permission{ProjectKey: "alpha", Username: "reader", CanView: true}
	if err := service.SavePermission(context.Background(), permission); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetPermission(context.Background(), "alpha", "reader")
	if err != nil || !stored.CanView || stored.CanDeploy || stored.CanConfigure || stored.CanViewSecrets {
		t.Fatalf("view-only permission was not preserved: %#v, %v", stored, err)
	}
	if err := service.SavePermission(context.Background(), Permission{ProjectKey: "alpha", Username: "reader"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPermission(context.Background(), "alpha", "reader"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected membership deletion, got %v", err)
	}
}

func TestNormalizeProjectKeyAcceptsUserFacingNames(t *testing.T) {
	tests := map[string]string{
		"kbp小游戏":      "kbp",
		"2026 游戏":     "p-2026",
		"Payment_API": "payment-api",
	}
	for input, expected := range tests {
		if actual := NormalizeProjectKey(input); actual != expected {
			t.Errorf("NormalizeProjectKey(%q) = %q, want %q", input, actual, expected)
		}
	}
	chineseOnly := NormalizeProjectKey("小游戏")
	if !ValidProjectKey(chineseOnly) || !strings.HasPrefix(chineseOnly, "project-") {
		t.Fatalf("Chinese-only project name produced invalid key %q", chineseOnly)
	}
}

func TestSaveProjectUpdatesDisplayFieldsWithoutChangingResourceKey(t *testing.T) {
	store := &accessStoreStub{
		projects:    map[string]Project{"kbp": {Key: "kbp", DisplayName: "旧名称", Description: "旧说明"}},
		permissions: make(map[string]Permission),
	}
	service := NewService(store)
	if err := service.SaveProject(context.Background(), Project{Key: "kbp", DisplayName: "新项目名称", Description: "新说明"}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetProject(context.Background(), "kbp")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Key != "kbp" || updated.DisplayName != "新项目名称" || updated.Description != "新说明" {
		t.Fatalf("unexpected updated project: %#v", updated)
	}
}

func TestSaveProjectRejectsOversizedDisplayFields(t *testing.T) {
	store := &accessStoreStub{projects: make(map[string]Project), permissions: make(map[string]Permission)}
	service := NewService(store)
	if err := service.SaveProject(context.Background(), Project{Key: "kbp", DisplayName: strings.Repeat("项", 129)}); err == nil {
		t.Fatal("expected oversized display name to be rejected")
	}
	if err := service.SaveProject(context.Background(), Project{Key: "kbp", DisplayName: "项目", Description: strings.Repeat("说", 1001)}); err == nil {
		t.Fatal("expected oversized description to be rejected")
	}
}
