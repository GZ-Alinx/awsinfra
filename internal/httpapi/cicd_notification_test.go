package httpapi

import (
	"errors"
	"net/http/httptest"
	"testing"

	"ops-deploy-platform/internal/cicd"
)

func TestManagedLarkCredentialIdentityIsStableAndScoped(t *testing.T) {
	key, externalID := managedLarkCredentialIdentity("project-a", "test-jenkins", "test", "release-alerts")
	keyAgain, externalIDAgain := managedLarkCredentialIdentity("project-a", "test-jenkins", "test", "release-alerts")
	if key != keyAgain || externalID != externalIDAgain {
		t.Fatalf("identity is not stable: %q/%q != %q/%q", key, externalID, keyAgain, externalIDAgain)
	}
	otherKey, otherExternalID := managedLarkCredentialIdentity("project-a", "prod-jenkins", "prod", "release-alerts")
	if key == otherKey || externalID == otherExternalID {
		t.Fatalf("identity is not scoped by connection and environment: %q/%q", otherKey, otherExternalID)
	}
	if len(key) > 63 || len(externalID) > 128 {
		t.Fatalf("identity exceeds Jenkins/platform limits: %q/%q", key, externalID)
	}
}

func TestCICDRequestEnvironmentIsMandatory(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/projects/project-a/cicd/connections?environment=prod", nil)
	if environment, err := cicdRequestEnvironment(request); err != nil || environment != "prod" {
		t.Fatalf("valid production scope = %q, %v", environment, err)
	}
	request = httptest.NewRequest("GET", "/api/projects/project-a/environments/test/cicd/connections", nil)
	request.SetPathValue("environment", "test")
	if environment, err := cicdRequestEnvironment(request); err != nil || environment != "test" {
		t.Fatalf("valid path scope = %q, %v", environment, err)
	}
	request = httptest.NewRequest("GET", "/api/projects/project-a/environments/test/cicd/connections?environment=prod", nil)
	request.SetPathValue("environment", "test")
	if _, err := cicdRequestEnvironment(request); !errors.Is(err, cicd.ErrConflict) {
		t.Fatalf("conflicting path and query scope was accepted: %v", err)
	}
	for _, raw := range []string{"", "production", "TEST%0Aprod"} {
		request = httptest.NewRequest("GET", "/api/projects/project-a/cicd/connections?environment="+raw, nil)
		if _, err := cicdRequestEnvironment(request); !errors.Is(err, cicd.ErrInvalid) {
			t.Fatalf("unsafe environment %q was accepted: %v", raw, err)
		}
	}

	request = httptest.NewRequest("GET", "/api/projects/project-a/cicd/connections", nil)
	if !cicdRequestEnvironmentMissing(request) {
		t.Fatal("missing environment was not detected")
	}
	request = httptest.NewRequest("GET", "/api/projects/project-a/cicd/connections?environment=prod", nil)
	if cicdRequestEnvironmentMissing(request) {
		t.Fatal("valid query environment was treated as missing")
	}
	request = httptest.NewRequest("GET", "/api/projects/project-a/environments/prod/cicd/connections", nil)
	request.SetPathValue("environment", "prod")
	if cicdRequestEnvironmentMissing(request) {
		t.Fatal("valid path environment was treated as missing")
	}
}
