package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/environment"
)

type awsQuotaPreflightExecutor struct {
	vCPUQuota        int
	usedVCPU         int
	eipQuota         int
	usedEIP          int
	clusterActive    bool
	nodeGroupDesired int
}

func (e awsQuotaPreflightExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	args := strings.Join(command.Args, " ")
	switch {
	case strings.Contains(args, "ec2 describe-instance-types"):
		_, _ = io.WriteString(output, `{"InstanceTypes":[{"InstanceType":"m5.large","VCpuInfo":{"DefaultVCpus":2}}]}`)
		return nil
	case strings.Contains(args, "eks describe-cluster"):
		if e.clusterActive {
			_, _ = io.WriteString(output, `{"cluster":{"status":"ACTIVE"}}`)
			return nil
		}
		return errors.New("ResourceNotFoundException: cluster does not exist")
	case strings.Contains(args, "eks describe-nodegroup"):
		if e.clusterActive {
			_, _ = fmt.Fprintf(output, `{"nodegroup":{"scalingConfig":{"desiredSize":%d}}}`, e.nodeGroupDesired)
			return nil
		}
		return errors.New("ResourceNotFoundException: node group does not exist")
	case strings.Contains(args, "service-quotas get-service-quota") && strings.Contains(args, "L-1216C47A"):
		_, _ = fmt.Fprintf(output, `{"Quota":{"Value":%d}}`, e.vCPUQuota)
		return nil
	case strings.Contains(args, "ec2 describe-instances"):
		instances := make([]string, 0, e.usedVCPU/2)
		for index := 0; index < e.usedVCPU/2; index++ {
			instances = append(instances, `{"InstanceType":"m5.large"}`)
		}
		// Spot instances deliberately appear in the inventory but must not reduce
		// the Standard On-Demand vCPU reserve.
		instances = append(instances, `{"InstanceType":"m5.large","InstanceLifecycle":"spot"}`)
		_, _ = fmt.Fprintf(output, `{"Reservations":[{"Instances":[%s]}]}`, strings.Join(instances, ","))
		return nil
	case strings.Contains(args, "service-quotas get-service-quota") && strings.Contains(args, "L-0263D0A3"):
		_, _ = fmt.Fprintf(output, `{"Quota":{"Value":%d}}`, e.eipQuota)
		return nil
	case strings.Contains(args, "ec2 describe-addresses"):
		addresses := make([]string, e.usedEIP)
		for index := range addresses {
			addresses[index] = `{}`
		}
		_, _ = fmt.Fprintf(output, `{"Addresses":[%s]}`, strings.Join(addresses, ","))
		return nil
	default:
		return fmt.Errorf("unexpected command: %s", args)
	}
}

func newEKSQuotaTestDeployment(executor commandExecutor) *Deployment {
	return &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{AWS: "aws"},
			Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace"},
		},
		executor: executor,
	}
}

func newEKSQuotaTestDocument() environment.Document {
	return environment.Document{
		"project":     "quota-test",
		"environment": "test",
		"region":      "ap-south-1",
		"eks": map[string]any{
			"node_groups": map[string]any{
				"application": map[string]any{
					"desired_size":   1,
					"instance_types": []any{"m5.large"},
				},
			},
		},
	}
}

func TestNewEKSQuotaPreflightUsesRemainingCapacity(t *testing.T) {
	deployment := newEKSQuotaTestDeployment(awsQuotaPreflightExecutor{
		vCPUQuota: 100,
		usedVCPU:  4,
		eipQuota:  10,
		usedEIP:   5,
	})
	var output strings.Builder
	if err := deployment.checkEKSNodeGroupVCPUQuota(context.Background(), newEKSQuotaTestDocument(), &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"实际剩余 96", "实际剩余 5", "配额预检通过"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("quota preflight output is missing %q:\n%s", expected, output.String())
		}
	}
}

func TestNewEKSQuotaPreflightBlocksInsufficientRemainingVCPU(t *testing.T) {
	deployment := newEKSQuotaTestDeployment(awsQuotaPreflightExecutor{
		vCPUQuota: 100,
		usedVCPU:  6,
		eipQuota:  10,
		usedEIP:   0,
	})
	err := deployment.checkEKSNodeGroupVCPUQuota(context.Background(), newEKSQuotaTestDocument(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "AWS_CREATE_QUOTA_INSUFFICIENT") || !strings.Contains(err.Error(), "实际剩余 94") {
		t.Fatalf("insufficient remaining vCPU must fail before Terraform, got %v", err)
	}
}

func TestNewEKSQuotaPreflightBlocksInsufficientRemainingEIP(t *testing.T) {
	deployment := newEKSQuotaTestDeployment(awsQuotaPreflightExecutor{
		vCPUQuota: 120,
		usedVCPU:  4,
		eipQuota:  9,
		usedEIP:   5,
	})
	err := deployment.checkEKSNodeGroupVCPUQuota(context.Background(), newEKSQuotaTestDocument(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "AWS_CREATE_QUOTA_INSUFFICIENT") || !strings.Contains(err.Error(), "实际剩余 4") {
		t.Fatalf("insufficient remaining EIP must fail before Terraform, got %v", err)
	}
}

func TestExistingActiveEKSDoesNotUseNewClusterReserveThreshold(t *testing.T) {
	deployment := newEKSQuotaTestDeployment(awsQuotaPreflightExecutor{
		clusterActive:    true,
		nodeGroupDesired: 1,
	})
	var output strings.Builder
	if err := deployment.checkEKSNodeGroupVCPUQuota(context.Background(), newEKSQuotaTestDocument(), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "不套用新建环境保留阈值") || !strings.Contains(output.String(), "没有新增初始容量") {
		t.Fatalf("existing ACTIVE EKS must bypass the new-cluster reserve policy:\n%s", output.String())
	}
}
