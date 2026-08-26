package kubetunnel

import (
	"testing"

	"github.com/GZ-Alinx/awsinfra/internal/cicd"
)

func TestValidateEndpoint(t *testing.T) {
	valid := cicd.ManagedEndpoint{
		ProjectKey: "demo", EnvironmentKey: "test", TargetName: "demo-test",
		Region: "ap-south-1", ClusterName: "demo-test-eks",
		Namespace: "demo-test-platform", ServiceName: "jenkins", ServicePort: 8080,
	}
	if err := validateEndpoint(valid); err != nil {
		t.Fatalf("valid endpoint rejected: %v", err)
	}

	invalid := []cicd.ManagedEndpoint{
		func() cicd.ManagedEndpoint { item := valid; item.Region = "ap-south-1;id"; return item }(),
		func() cicd.ManagedEndpoint { item := valid; item.ClusterName = "demo/test"; return item }(),
		func() cicd.ManagedEndpoint { item := valid; item.Namespace = "Platform_Server"; return item }(),
		func() cicd.ManagedEndpoint { item := valid; item.ServiceName = "jenkins;id"; return item }(),
		func() cicd.ManagedEndpoint { item := valid; item.ServicePort = 0; return item }(),
	}
	for index, endpoint := range invalid {
		if err := validateEndpoint(endpoint); err == nil {
			t.Fatalf("invalid endpoint %d was accepted: %#v", index, endpoint)
		}
	}
}

func TestRemoveEnvironmentKeys(t *testing.T) {
	got := removeEnvironmentKeys([]string{
		"PATH=/usr/bin", "AWS_ACCESS_KEY_ID=wrong", "AWS_PROFILE=wrong", "HOME=/tmp",
	}, "AWS_ACCESS_KEY_ID", "AWS_PROFILE")
	if len(got) != 2 || got[0] != "PATH=/usr/bin" || got[1] != "HOME=/tmp" {
		t.Fatalf("unexpected sanitized environment: %#v", got)
	}
}
