package httpapi

import (
	"testing"

	"ops-deploy-platform/internal/access"
	"ops-deploy-platform/internal/awscredentials"
)

func TestVisibleAWSCredentialsKeepsArchivedCredentialsForManagers(t *testing.T) {
	items := []awscredentials.PublicInfo{
		{Key: "active", ProjectKey: "active-project"},
		{Key: "archived", ProjectKey: "archived-project", ProjectArchived: true},
	}
	projects := []access.Project{{Key: "active-project"}}

	managerView := visibleAWSCredentials(items, projects, true)
	if len(managerView) != 2 || managerView[1].Key != "archived" || !managerView[1].ProjectArchived {
		t.Fatalf("credential manager lost archived credential: %#v", managerView)
	}

	memberView := visibleAWSCredentials(items, projects, false)
	if len(memberView) != 1 || memberView[0].Key != "active" {
		t.Fatalf("project member saw credentials outside active project access: %#v", memberView)
	}
}
