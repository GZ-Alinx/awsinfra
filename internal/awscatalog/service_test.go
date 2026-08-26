package awscatalog

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseEKSVersionsSortsNewestFirst(t *testing.T) {
	payload := []byte(`{"clusterVersions":[{"clusterVersion":"1.31","versionStatus":"EXTENDED_SUPPORT"},{"clusterVersion":"1.33","defaultVersion":true,"versionStatus":"STANDARD_SUPPORT"},{"clusterVersion":"1.32","status":"standard-support"}]}`)
	versions, err := parseEKSVersions(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 || versions[0].Version != "1.33" || !versions[0].Default || versions[1].Version != "1.32" || versions[1].Status != "STANDARD_SUPPORT" {
		t.Fatalf("unexpected versions: %#v", versions)
	}
}

func TestParseInstanceTypesReturnsResourceSizing(t *testing.T) {
	payload := []byte(`{"InstanceTypes":[{"InstanceType":"m7i.xlarge","CurrentGeneration":true,"VCpuInfo":{"DefaultVCpus":4},"MemoryInfo":{"SizeInMiB":16384},"ProcessorInfo":{"SupportedArchitectures":["x86_64"]},"NetworkInfo":{"NetworkPerformance":"Up to 12.5 Gigabit","MaximumNetworkInterfaces":4},"EbsInfo":{"EbsOptimizedSupport":"default"},"SupportedUsageClasses":["on-demand","spot"]},{"InstanceType":"m7i.large","VCpuInfo":{"DefaultVCpus":2},"MemoryInfo":{"SizeInMiB":8192}}]}`)
	items, err := parseInstanceTypes(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "m7i.large" || items[0].VCpu != 2 || items[1].MemoryMiB != 16384 || items[1].NetworkPerformance == "" {
		t.Fatalf("unexpected instance types: %#v", items)
	}
}

func TestParseOrderableDBInstanceOptionsDeduplicatesClasses(t *testing.T) {
	payload := []byte(`{"OrderableDBInstanceOptions":[{"DBInstanceClass":"db.t4g.medium","EngineVersion":"8.0","MultiAZCapable":true,"StorageType":"gp3","AvailabilityZones":[{"Name":"ap-south-1a"}]},{"DBInstanceClass":"db.t4g.medium","EngineVersion":"8.0","StorageType":"io1","AvailabilityZones":[{"Name":"ap-south-1b"}]},{"DBInstanceClass":"db.r7g.large","EngineVersion":"8.0"}]}`)
	items, err := parseOrderableDBInstanceOptions(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Value != "db.r7g.large" || items[1].Value != "db.t4g.medium" || len(items[1].AvailabilityZones) != 2 || !items[1].MultiAZCapable {
		t.Fatalf("unexpected database options: %#v", items)
	}
}

func TestParseMQInstanceOptionsIncludesSupportedModes(t *testing.T) {
	payload := []byte(`{"BrokerInstanceOptions":[{"HostInstanceType":"mq.m7g.medium","SupportedEngineVersions":["3.13"],"SupportedDeploymentModes":["SINGLE_INSTANCE","CLUSTER_MULTI_AZ"],"StorageType":"EBS","AvailabilityZones":[{"Name":"ap-south-1a"}]}]}`)
	items, err := parseMQInstanceOptions(payload)
	if err != nil || len(items) != 1 || len(items[0].DeploymentModes) != 2 || items[0].Value != "mq.m7g.medium" {
		t.Fatalf("unexpected MQ options: %#v, err=%v", items, err)
	}
}

func TestParseEKSClustersSortsAndDeduplicates(t *testing.T) {
	items, err := parseEKSClusters([]byte(`{"clusters":["shared-prod","demo-2","demo-1","demo-1"]}`))
	if err != nil || len(items) != 3 || items[0].Name != "demo-1" || items[2].Name != "shared-prod" {
		t.Fatalf("unexpected EKS clusters: %#v, err=%v", items, err)
	}
}

func TestParseVPCsGroupsAndSortsSubnets(t *testing.T) {
	vpcs := []byte(`{"Vpcs":[{"VpcId":"vpc-2","CidrBlock":"10.2.0.0/16","State":"available","Tags":[{"Key":"Name","Value":"shared"}]},{"VpcId":"vpc-1","CidrBlock":"10.1.0.0/16","State":"pending"},{"VpcId":"vpc-3","CidrBlock":"10.3.0.0/16","State":"available","IsDefault":true}]}`)
	subnets := []byte(`{"Subnets":[{"SubnetId":"subnet-b","VpcId":"vpc-2","CidrBlock":"10.2.2.0/24","AvailabilityZone":"ap-south-1b","AvailableIpAddressCount":120},{"SubnetId":"subnet-a","VpcId":"vpc-2","CidrBlock":"10.2.1.0/24","AvailabilityZone":"ap-south-1a","AvailableIpAddressCount":200,"MapPublicIpOnLaunch":true,"Tags":[{"Key":"Name","Value":"public-a"}]}]}`)
	items, err := parseVPCs(vpcs, subnets)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "shared" || items[1].ID != "vpc-3" || len(items[0].Subnets) != 2 {
		t.Fatalf("unexpected VPC catalog: %#v", items)
	}
	if items[0].Subnets[0].ID != "subnet-a" || !items[0].Subnets[0].MapPublicIPOnLaunch || items[0].Subnets[0].AvailableIPCount != 200 {
		t.Fatalf("unexpected subnet catalog: %#v", items[0].Subnets)
	}
}

func TestParseSecurityGroupsIncludesVPCAndDisplayName(t *testing.T) {
	payload := []byte(`{"SecurityGroups":[{"GroupId":"sg-0bbb2222","GroupName":"workers","VpcId":"vpc-01234567","Description":"worker access","IpPermissions":[{"IpProtocol":"tcp","FromPort":443,"ToPort":443,"IpRanges":[{"CidrIp":"10.0.0.0/8"}]}]},{"GroupId":"sg-0aaa1111","GroupName":"frontend","VpcId":"vpc-01234567","Description":"NLB frontend","Tags":[{"Key":"Name","Value":"public-gateway"}],"IpPermissions":[{"IpProtocol":"tcp","FromPort":80,"ToPort":443,"IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]},{"GroupId":"sg-0ccc3333","GroupName":"default","VpcId":"vpc-01234567"},{"GroupId":"invalid","GroupName":"ignored","VpcId":"vpc-01234567"}]}`)
	items, err := parseSecurityGroups(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].ID != "sg-0ccc3333" || items[1].ID != "sg-0aaa1111" || items[1].DisplayName != "public-gateway" || items[1].VPCID != "vpc-01234567" || items[2].Name != "workers" {
		t.Fatalf("unexpected security groups: %#v", items)
	}
	if items[0].Selectable || !strings.Contains(items[0].BlockedReason, "默认安全组") {
		t.Fatalf("default security group was not blocked: %#v", items[0])
	}
	if !items[1].AllowsHTTP || !items[1].AllowsHTTPS || !items[1].PublicHTTP || !items[1].PublicHTTPS || items[1].IngressSourceCount != 1 {
		t.Fatalf("public frontend rule summary is incorrect: %#v", items[1])
	}
	if items[2].AllowsHTTP || !items[2].AllowsHTTPS || items[2].PublicHTTPS {
		t.Fatalf("private HTTPS rule summary is incorrect: %#v", items[2])
	}
}

func TestParsePricingInstanceOptionsFiltersServicePrefix(t *testing.T) {
	product := `{"product":{"attributes":{"instanceType":"cache.r7g.large","regionCode":"ap-south-1"}}}`
	payload, _ := json.Marshal(map[string]any{"PriceList": []string{product, `{"product":{"attributes":{"instanceType":"unrelated.large"}}}`}})
	items, err := parsePricingInstanceOptions(payload, "cache.")
	if err != nil || len(items) != 1 || items[0].Value != "cache.r7g.large" {
		t.Fatalf("unexpected pricing options: %#v, err=%v", items, err)
	}
}

func TestParsePricingInstanceOptionsNormalizesMSKBrokerType(t *testing.T) {
	product := `{"product":{"attributes":{"computeFamily":"m7g.large","regionCode":"ap-south-1"}}}`
	payload, _ := json.Marshal(map[string]any{"PriceList": []string{product, `{"product":{"attributes":{"instanceType":"No Instance Type"}}}`}})
	items, err := parsePricingInstanceOptions(payload, "kafka.")
	if err != nil || len(items) != 1 || items[0].Value != "kafka.m7g.large" {
		t.Fatalf("unexpected MSK pricing options: %#v, err=%v", items, err)
	}
}

func TestSelectLatestEngineVersionAcceptsMajorVersion(t *testing.T) {
	payload := []byte(`{"DBEngineVersions":[{"EngineVersion":"8.0.41"},{"EngineVersion":"8.0.43"},{"EngineVersion":"8.4.6"}]}`)
	version, err := selectLatestEngineVersion(payload, "8.0")
	if err != nil || version != "8.0.43" {
		t.Fatalf("latest engine version = %q, err=%v", version, err)
	}
	if _, err := selectLatestEngineVersion(payload, "5.7"); !errors.Is(err, ErrEngineVersionMissing) {
		t.Fatalf("missing version error = %v", err)
	}
}

func TestParseEngineVersionsAcrossAWSServices(t *testing.T) {
	tests := []struct {
		service string
		payload string
		want    []string
	}{
		{"rds-mysql", `{"DBEngineVersions":[{"EngineVersion":"8.0.41"},{"EngineVersion":"8.4.6"}]}`, []string{"8.4.6", "8.0.41"}},
		{"elasticache", `{"CacheEngineVersions":[{"EngineVersion":"7.1"},{"EngineVersion":"8.2"}]}`, []string{"8.2", "7.1"}},
		{"msk", `{"KafkaVersions":[{"Version":"3.9.x","Status":"ACTIVE"},{"Version":"3.8.x","Status":"DEPRECATED"}]}`, []string{"3.9.x"}},
		{"amazon-mq", `{"BrokerInstanceOptions":[{"SupportedEngineVersions":["3.13","3.12"]}]}`, []string{"3.13", "3.12"}},
	}
	for _, test := range tests {
		t.Run(test.service, func(t *testing.T) {
			got, err := parseEngineVersions([]byte(test.payload), test.service)
			if err != nil || !slices.Equal(got, test.want) {
				t.Fatalf("versions = %#v, err = %v, want %#v", got, err, test.want)
			}
		})
	}
}

func TestCatalogInputValidation(t *testing.T) {
	service := &Service{}
	if _, err := service.EKSVersions(t.Context(), "demo", "invalid"); err != ErrInvalidRegion {
		t.Fatalf("invalid region error = %v", err)
	}
	if _, err := service.EKSClusters(t.Context(), "demo", "invalid"); err != ErrInvalidRegion {
		t.Fatalf("invalid EKS cluster region error = %v", err)
	}
	if _, err := service.VPCs(t.Context(), "demo", "invalid"); err != ErrInvalidRegion {
		t.Fatalf("invalid VPC region error = %v", err)
	}
	if _, err := service.SecurityGroups(t.Context(), "demo", "ap-south-1", "vpc-not-valid"); err != ErrInvalidQuery {
		t.Fatalf("invalid security group VPC query error = %v", err)
	}
	if _, err := service.InstanceTypes(t.Context(), "demo", "ap-south-1", "m7i;rm"); err != ErrInvalidQuery {
		t.Fatalf("invalid query error = %v", err)
	}
}

func TestNormalizeECRRepositoryAcceptsNameAndCurrentRegistry(t *testing.T) {
	tests := []struct {
		value        string
		wantName     string
		wantRegistry string
	}{
		{value: "kbp/gateway", wantName: "kbp/gateway"},
		{value: "123456789012.dkr.ecr.ap-south-1.amazonaws.com/kbp/game-admin", wantName: "kbp/game-admin", wantRegistry: "123456789012.dkr.ecr.ap-south-1.amazonaws.com"},
	}
	for _, test := range tests {
		name, registry, err := normalizeECRRepository(test.value, "ap-south-1")
		if err != nil || name != test.wantName || registry != test.wantRegistry {
			t.Fatalf("normalizeECRRepository(%q)=(%q,%q,%v), want (%q,%q,nil)", test.value, name, registry, err, test.wantName, test.wantRegistry)
		}
	}
}

func TestNormalizeECRRepositoryRejectsUnsafeOrForeignValues(t *testing.T) {
	for _, value := range []string{
		"docker.io/library/nginx",
		"123456789012.dkr.ecr.us-east-1.amazonaws.com/kbp/gateway",
		"kbp/Gateway",
		"kbp/gateway:latest",
		"https://123456789012.dkr.ecr.ap-south-1.amazonaws.com/kbp/gateway",
	} {
		if _, _, err := normalizeECRRepository(value, "ap-south-1"); !errors.Is(err, ErrInvalidECRRepository) {
			t.Fatalf("normalizeECRRepository(%q) error = %v, want ErrInvalidECRRepository", value, err)
		}
	}
}

func TestParseECRRepositorySupportsDescribeAndCreateResponses(t *testing.T) {
	described, err := parseECRRepository([]byte(`{"repositories":[{"repositoryName":"kbp/gateway","repositoryUri":"123456789012.dkr.ecr.ap-south-1.amazonaws.com/kbp/gateway"}]}`), false)
	if err != nil || described.Name != "kbp/gateway" || described.Created {
		t.Fatalf("described ECR repository = %#v, err=%v", described, err)
	}
	created, err := parseECRRepository([]byte(`{"repository":{"repositoryName":"kbp/gateway","repositoryUri":"123456789012.dkr.ecr.ap-south-1.amazonaws.com/kbp/gateway"}}`), true)
	if err != nil || created.URI == "" || !created.Created {
		t.Fatalf("created ECR repository = %#v, err=%v", created, err)
	}
}

func TestCatalogRequiresExplicitProjectCredential(t *testing.T) {
	service := &Service{tool: "/usr/bin/false"}
	if _, err := service.EKSVersions(t.Context(), "demo", "ap-south-1"); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("missing project credential error = %v", err)
	}
}

func TestClassifyCommandError(t *testing.T) {
	for _, test := range []struct {
		name       string
		contextErr error
		stderr     string
		want       error
	}{
		{name: "missing credential", stderr: "Unable to locate credentials. You can configure credentials by running aws configure.", want: ErrCredentialUnavailable},
		{name: "expired credential", stderr: "An error occurred (ExpiredToken) when calling the operation", want: ErrCredentialRejected},
		{name: "expired aws login", stderr: "Your session has expired. Please reauthenticate using 'aws login'.", want: ErrCredentialRejected},
		{name: "permission", stderr: "AccessDeniedException: not authorized to perform eks:DescribeClusterVersions", want: ErrAccessDenied},
		{name: "old cli", stderr: "Invalid choice: 'describe-cluster-versions'", want: ErrUnsupportedCLI},
		{name: "network", stderr: "Could not connect to the endpoint URL: https://eks.ap-south-1.amazonaws.com", want: ErrNetworkUnavailable},
		{name: "timeout", contextErr: context.DeadlineExceeded, want: ErrQueryTimedOut},
		{name: "unknown", stderr: "unexpected AWS failure", want: ErrQueryFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyCommandError(test.contextErr, test.stderr); !errors.Is(got, test.want) {
				t.Fatalf("classifyCommandError()=%v, want %v", got, test.want)
			}
		})
	}
}
