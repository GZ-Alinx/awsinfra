package httpapi

import (
	"testing"
	"time"

	"ops-deploy-platform/internal/jobs"
	statusservice "ops-deploy-platform/internal/status"
)

func TestDeriveEnvironmentLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 16, 2, 0, 0, 0, time.UTC)
	finished := now.Add(time.Minute)
	tests := []struct {
		name   string
		jobs   []jobs.Job
		report *statusservice.Report
		want   string
	}{
		{name: "configured environment is ready", want: "ready"},
		{
			name:   "active deployment has priority over cached health",
			jobs:   []jobs.Job{{ID: "deploying", Action: jobs.ActionDeploy, Status: jobs.StatusRunning, CreatedAt: now, Progress: 42}},
			report: &statusservice.Report{Cluster: statusservice.Cluster{Status: "ACTIVE", Reachable: true}},
			want:   "deploying",
		},
		{
			name: "successful deployment is running",
			jobs: []jobs.Job{{ID: "deployed", Action: jobs.ActionDeploy, Status: jobs.StatusSucceeded, CreatedAt: now, FinishedAt: &finished, Progress: 100}},
			want: "running",
		},
		{
			name:   "successful destroy remains authoritative over stale cache",
			jobs:   []jobs.Job{{ID: "destroyed", Action: jobs.ActionDestroy, Status: jobs.StatusSucceeded, CreatedAt: now, FinishedAt: &finished, Progress: 100}},
			report: &statusservice.Report{Cluster: statusservice.Cluster{Status: "ACTIVE", Reachable: true}},
			want:   "destroyed",
		},
		{
			name: "failed component deployment links to failure",
			jobs: []jobs.Job{{ID: "failed", Action: jobs.ActionPlatform, Status: jobs.StatusFailed, CreatedAt: now, FinishedAt: &finished}},
			want: "component_failed",
		},
		{
			name: "active TLS update is configuring",
			jobs: []jobs.Job{{ID: "tls", Action: jobs.ActionTLS, Status: jobs.StatusRunning, CreatedAt: now, Progress: 50}},
			want: "configuring",
		},
		{
			name:   "cached active cluster restores status after history cleanup",
			report: &statusservice.Report{ObservedAt: now, Cluster: statusservice.Cluster{Status: "ACTIVE", Reachable: true}},
			want:   "running",
		},
		{
			name: "successful plan after failed deploy becomes ready",
			jobs: []jobs.Job{
				{ID: "plan", Action: jobs.ActionPlan, Status: jobs.StatusSucceeded, CreatedAt: now.Add(time.Minute), FinishedAt: &finished},
				{ID: "failed", Action: jobs.ActionDeploy, Status: jobs.StatusFailed, CreatedAt: now},
			},
			want: "ready",
		},
		{
			name: "ignored failure does not override last successful deployment",
			jobs: []jobs.Job{
				{ID: "ignored", Action: jobs.ActionPlatform, Status: jobs.StatusIgnored, CreatedAt: now.Add(time.Minute), FinishedAt: &finished},
				{ID: "deployed", Action: jobs.ActionPlatform, Status: jobs.StatusSucceeded, CreatedAt: now, FinishedAt: &finished, Progress: 100},
			},
			want: "running",
		},
		{
			name: "only ignored failure returns to ready without claiming success",
			jobs: []jobs.Job{{ID: "ignored", Action: jobs.ActionPlatform, Status: jobs.StatusIgnored, CreatedAt: now, FinishedAt: &finished}},
			want: "ready",
		},
		{
			name:   "missing expected cluster is abnormal",
			jobs:   []jobs.Job{{ID: "deployed", Action: jobs.ActionDeploy, Status: jobs.StatusSucceeded, CreatedAt: now, FinishedAt: &finished}},
			report: &statusservice.Report{ObservedAt: now, Cluster: statusservice.Cluster{Status: "NOT_FOUND"}},
			want:   "abnormal",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := deriveEnvironmentLifecycle(test.jobs, test.report)
			if got.Status != test.want {
				t.Fatalf("status = %q, want %q", got.Status, test.want)
			}
		})
	}
}

func TestExistingEKSWithoutProjectDeploymentStaysReady(t *testing.T) {
	report := &statusservice.Report{Cluster: statusservice.Cluster{Status: "ACTIVE", Reachable: true}}
	state := deriveEnvironmentLifecycleForTarget(nil, report, true)
	if state.Status != "ready" {
		t.Fatalf("shared cluster status was mistaken for project resources: %#v", state)
	}

	now := time.Now()
	finished := now
	state = deriveEnvironmentLifecycleForTarget([]jobs.Job{{
		ID: "platform", Action: jobs.ActionPlatform, Status: jobs.StatusSucceeded,
		CreatedAt: now, FinishedAt: &finished, Progress: 100,
	}}, report, true)
	if state.Status != "running" {
		t.Fatalf("deployed project components were not reported as running: %#v", state)
	}
}

func TestLifecycleFromActiveJobPreservesNavigationMetadata(t *testing.T) {
	created := time.Date(2026, 7, 16, 3, 0, 0, 0, time.UTC)
	state := deriveEnvironmentLifecycle([]jobs.Job{{
		ID: "job-123", Action: jobs.ActionDestroy, Status: jobs.StatusRunning,
		CreatedAt: created, Progress: 67,
	}}, nil)
	if state.Status != "destroying" || state.LatestJobID != "job-123" || state.Progress != 67 {
		t.Fatalf("unexpected lifecycle metadata: %#v", state)
	}
}

func TestDeriveDeploymentPhases(t *testing.T) {
	now := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	started := now.Add(30 * time.Second)
	tests := []struct {
		name        string
		jobs        []jobs.Job
		report      *statusservice.Report
		existingEKS bool
		wantOne     bool
		wantTwo     bool
	}{
		{name: "new environment"},
		{name: "managed cluster recovered from status", report: &statusservice.Report{Cluster: statusservice.Cluster{Status: "ACTIVE", Reachable: true}}, wantOne: true},
		{name: "shared EKS is not a project deployment", existingEKS: true, report: &statusservice.Report{Cluster: statusservice.Cluster{Status: "ACTIVE", Reachable: true}}},
		{name: "both phases succeeded", jobs: []jobs.Job{
			{Action: jobs.ActionPlatform, Status: jobs.StatusSucceeded, CreatedAt: now.Add(time.Minute)},
			{Action: jobs.ActionDeploy, Status: jobs.StatusSucceeded, CreatedAt: now},
		}, wantOne: true, wantTwo: true},
		{name: "failed phase after apply started is an update", jobs: []jobs.Job{{Action: jobs.ActionPlatform, Status: jobs.StatusFailed, SuccessSteps: 4, CreatedAt: now, Steps: []jobs.Step{{Name: phaseTwoMutation, Status: jobs.StepFailed, StartedAt: &started}}}}, wantTwo: true},
		{name: "failed phase one apply may own partial resources", jobs: []jobs.Job{{Action: jobs.ActionDeploy, Status: jobs.StatusFailed, SuccessSteps: 10, CreatedAt: now, Steps: []jobs.Step{{Name: phaseOneInfraMutation, Status: jobs.StepFailed, StartedAt: &started}}}}, wantOne: true},
		{name: "failure after validation but before apply is not deployed", jobs: []jobs.Job{{Action: jobs.ActionDeploy, Status: jobs.StatusFailed, SuccessSteps: 8, CreatedAt: now, Steps: []jobs.Step{{Name: "校验 AWS 基础资源 Terraform", Status: jobs.StepSucceeded, StartedAt: &started}}}}},
		{name: "successful destroy resets both phases even with stale status", report: &statusservice.Report{Cluster: statusservice.Cluster{Status: "ACTIVE", Reachable: true}}, jobs: []jobs.Job{
			{Action: jobs.ActionDestroy, Status: jobs.StatusSucceeded, CreatedAt: now.Add(2 * time.Minute)},
			{Action: jobs.ActionPlatform, Status: jobs.StatusSucceeded, CreatedAt: now.Add(time.Minute)},
			{Action: jobs.ActionDeploy, Status: jobs.StatusSucceeded, CreatedAt: now},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			one, two := deriveDeploymentPhases(test.jobs, test.report, test.existingEKS)
			if one != test.wantOne || two != test.wantTwo {
				t.Fatalf("phases = (%v, %v), want (%v, %v)", one, two, test.wantOne, test.wantTwo)
			}
		})
	}
}

func TestShouldResetMissingCloudBaselineOnlyAfterLatestSuccessfulDestroy(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		items []jobs.Job
		want  bool
	}{
		{name: "no history"},
		{name: "successful destroy", items: []jobs.Job{{Action: jobs.ActionDestroy, Status: jobs.StatusSucceeded, CreatedAt: now}}, want: true},
		{name: "plan after destroy is read only", items: []jobs.Job{
			{Action: jobs.ActionPlan, Status: jobs.StatusSucceeded, CreatedAt: now.Add(time.Minute)},
			{Action: jobs.ActionDestroy, Status: jobs.StatusSucceeded, CreatedAt: now},
		}, want: true},
		{name: "new deployment owns next generation", items: []jobs.Job{
			{Action: jobs.ActionDeploy, Status: jobs.StatusFailed, CreatedAt: now.Add(time.Minute)},
			{Action: jobs.ActionDestroy, Status: jobs.StatusSucceeded, CreatedAt: now},
		}},
		{name: "failed destroy is not a boundary", items: []jobs.Job{{Action: jobs.ActionDestroy, Status: jobs.StatusFailed, CreatedAt: now}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldResetMissingCloudBaseline(test.items); got != test.want {
				t.Fatalf("reset = %v, want %v", got, test.want)
			}
		})
	}
}
