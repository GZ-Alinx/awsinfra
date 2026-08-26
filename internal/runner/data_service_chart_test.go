package runner

import (
	"os"
	"strings"
	"testing"

	"github.com/GZ-Alinx/awsinfra/internal/environment"
)

func TestMySQLChartPreparesDedicatedPVCSubdirectory(t *testing.T) {
	payload, err := os.ReadFile("../../terraform/platform/charts/data-service/templates/statefulset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	template := string(payload)
	for _, required := range []string{
		"name: prepare-mysql-data",
		"target=/volume/mysql",
		"subPath: mysql",
		"helm.sh/resource-policy: keep",
		"refusing to clean a non-empty directory without MySQL data markers",
	} {
		if !strings.Contains(template, required) {
			t.Fatalf("MySQL PVC safety template is missing %q", required)
		}
	}
	if strings.Contains(template, "rm -rf") {
		t.Fatal("MySQL PVC initialization must never recursively delete retained data")
	}
}

func TestEtcdWorkbenchChartUsesNativeAuthenticationAndPersistentData(t *testing.T) {
	secret, err := os.ReadFile("../../terraform/platform/charts/etcd-workbench/templates/secret.yaml")
	if err != nil {
		t.Fatal(err)
	}
	statefulSet, err := os.ReadFile("../../terraform/platform/charts/etcd-workbench/templates/statefulset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(secret) + string(statefulSet)
	for _, required := range []string{
		"enable = true",
		"user = {{ .Values.basicAuth.username }}:{{ .Values.basicAuth.password }}",
		"configEncryptKey = {{ .Values.encryptionKey }}",
		"automountServiceAccountToken: false",
		"runAsNonRoot: true",
		"persistentVolumeClaimRetentionPolicy:",
		"helm.sh/resource-policy: keep",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Etcd Workbench safety template is missing %q", required)
		}
	}
}

func TestInterruptedBuiltInDataServiceIsSafelyRepaired(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.mysql.enabled", true)
	releases := []helmListRelease{{Name: "mysql", Namespace: "platform-server", Status: "pending-install"}}

	repairs, conflicts := interruptedDataServiceRepairs(doc, "random_password.data_service[\"mysql\"]", releases)
	if len(conflicts) != 0 || len(repairs) != 1 {
		t.Fatalf("repairs=%#v conflicts=%#v", repairs, conflicts)
	}
	if repairs[0].Key != "mysql" || repairs[0].Status != "pending-install" {
		t.Fatalf("unexpected repair: %#v", repairs[0])
	}
	if !repairs[0].FreshInstall {
		t.Fatal("an untracked pending-install release must be treated as a fresh installation")
	}
}

func TestFreshInstallPVCNamesOnlySelectsExactReleaseOrdinals(t *testing.T) {
	payload := []byte(`{"items":[{"metadata":{"name":"data-mysql-2"}},{"metadata":{"name":"data-other-0"}},{"metadata":{"name":"data-mysql-backup"}},{"metadata":{"name":"data-mysql-0"}}]}`)
	names, err := freshInstallPVCNames("mysql", payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "data-mysql-0,data-mysql-2" {
		t.Fatalf("unexpected PVC selection: %#v", names)
	}
}

func TestFailedUntrackedReleasePreservesPVC(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.mysql.enabled", true)
	repairs, conflicts := interruptedDataServiceRepairs(doc, "", []helmListRelease{{Name: "mysql", Namespace: "platform-server", Status: "failed"}})
	if len(conflicts) != 0 || len(repairs) != 1 || repairs[0].FreshInstall {
		t.Fatalf("failed release must preserve PVC unless it is explicitly pending-install: repairs=%#v conflicts=%#v", repairs, conflicts)
	}
}

func TestInterruptedBuiltInRabbitMQIsSafelyRepaired(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.rabbitmq.enabled", true)
	repairs, conflicts := interruptedDataServiceRepairs(doc, "", []helmListRelease{{Name: "rabbitmq", Namespace: "platform-server", Status: "pending-install"}})
	if len(conflicts) != 0 || len(repairs) != 1 || repairs[0].Key != "rabbitmq" || !repairs[0].FreshInstall {
		t.Fatalf("interrupted RabbitMQ was not selected for safe repair: repairs=%#v conflicts=%#v", repairs, conflicts)
	}
}

func TestTrackedFailedRabbitMQRollsBackWithoutUninstall(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.rabbitmq.enabled", true)
	state := `helm_release.catalog["rabbitmq"]`
	repairs := trackedInterruptedCatalogRepairs(doc, state, []helmListRelease{{Name: "rabbitmq", Namespace: "platform-server", Status: "failed"}})
	if len(repairs) != 1 || repairs[0].Key != "rabbitmq" || repairs[0].Status != "failed" || repairs[0].FreshInstall {
		t.Fatalf("tracked failed RabbitMQ must be rolled back in place: %#v", repairs)
	}
	if got := trackedInterruptedCatalogRepairs(doc, state, []helmListRelease{{Name: "rabbitmq", Namespace: "platform-server", Status: "deployed"}}); len(got) != 0 {
		t.Fatalf("healthy tracked release must not be rolled back: %#v", got)
	}
}

func TestFailedReleaseRecoveryDoesNotForceHistoricalPVCRollback(t *testing.T) {
	payload, err := os.ReadFile("deployment.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, required := range []string{
		`strings.EqualFold(strings.TrimSpace(repair.Status), "failed")`,
		`原地增量更新收敛`,
		`不卸载 Release、不修改或替换 PVC`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("failed release recovery is missing %q", required)
		}
	}
}

func TestTrackedBundledLogStackCanReinstallOnlyWhenPVCsAreRetained(t *testing.T) {
	for _, key := range []string{"clickvisual_stack", "efk_stack"} {
		t.Run(key, func(t *testing.T) {
			doc := environment.DefaultDocument("demo", "test")
			releaseName, _ := environment.GetPath(doc, "components.catalog."+key+".release_name")
			namespace, _ := environment.GetPath(doc, "components.catalog."+key+".namespace")
			repair := interruptedDataService{Key: key, Release: releaseName.(string), Namespace: namespace.(string), Status: "failed"}
			if !trackedCatalogReleaseSupportsPVCPreservingReinstall(doc, repair) {
				t.Fatalf("bundled %s with retained PVCs must support safe first-install recovery", key)
			}

			retentionPath := "components.catalog." + key + ".values.storage.retainOnDelete"
			if key == "efk_stack" {
				retentionPath = "components.catalog.efk_stack.values.elasticsearch.storage.retainOnDelete"
			}
			environment.SetPath(doc, retentionPath, false)
			if trackedCatalogReleaseSupportsPVCPreservingReinstall(doc, repair) {
				t.Fatalf("bundled %s without PVC retention must fail closed", key)
			}
		})
	}
}

func TestTrackedCustomOrStatefulComponentCannotBeAutomaticallyReinstalled(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	if !trackedCatalogReleaseSupportsPVCPreservingReinstall(doc, interruptedDataService{Key: "bytebase"}) {
		t.Fatal("platform-managed Bytebase with retained PVCs must support safe first-install recovery")
	}
	environment.SetPath(doc, "components.catalog.bytebase.values.persistence.retainOnDelete", false)
	if trackedCatalogReleaseSupportsPVCPreservingReinstall(doc, interruptedDataService{Key: "bytebase"}) {
		t.Fatal("Bytebase without retained PVCs must fail closed")
	}
	environment.SetPath(doc, "components.catalog.clickvisual_stack.builtin_chart", "custom-clickvisual")
	if trackedCatalogReleaseSupportsPVCPreservingReinstall(doc, interruptedDataService{Key: "clickvisual_stack"}) {
		t.Fatal("a custom chart must never be automatically uninstalled")
	}
	if trackedCatalogReleaseSupportsPVCPreservingReinstall(doc, interruptedDataService{Key: "rabbitmq"}) {
		t.Fatal("a stateful component without an explicit safe recovery policy must fail closed")
	}
}

func TestRabbitMQChartKeepsPVCWithoutMutatingClaimTemplateMetadata(t *testing.T) {
	payload, err := os.ReadFile("../../terraform/platform/charts/rabbitmq/templates/statefulset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	template := string(payload)
	if !strings.Contains(template, `whenDeleted: {{ ternary "Retain" "Delete" .Values.persistence.retainOnDelete }}`) {
		t.Fatal("RabbitMQ StatefulSet must preserve PVCs through persistentVolumeClaimRetentionPolicy")
	}
	claimTemplate := template[strings.Index(template, "volumeClaimTemplates:"):]
	if strings.Contains(claimTemplate, "helm.sh/resource-policy") {
		t.Fatal("RabbitMQ volumeClaimTemplates metadata is immutable and must not receive Helm retention annotations")
	}
}

func TestRabbitMQChartSupportsProductionClusterFormation(t *testing.T) {
	statefulSetPayload, err := os.ReadFile("../../terraform/platform/charts/rabbitmq/templates/statefulset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	configPayload, err := os.ReadFile("../../terraform/platform/charts/rabbitmq/templates/configmap.yaml")
	if err != nil {
		t.Fatal(err)
	}
	validationPayload, err := os.ReadFile("../../terraform/platform/charts/rabbitmq/templates/validate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(statefulSetPayload) + string(configPayload)
	for _, required := range []string{
		"podManagementPolicy: Parallel",
		"rabbitmq_peer_discovery_k8s",
		"cluster_formation.peer_discovery_backend = k8s",
		"RABBITMQ_USE_LONGNAME",
		"RABBITMQ_NODENAME",
		"topology.kubernetes.io/zone",
		"startupProbe:",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("RabbitMQ cluster support is missing %q", required)
		}
	}
	if strings.Contains(string(validationPayload), "supports standalone mode only") {
		t.Fatal("RabbitMQ validation still rejects clustered deployments")
	}
}

func TestInterruptedDatabaseConsoleFreshInstallIsSafelyRepaired(t *testing.T) {
	for _, key := range []string{"bytebase", "redisinsight", "etcd_workbench"} {
		t.Run(key, func(t *testing.T) {
			doc := environment.DefaultDocument("demo", "test")
			environment.SetPath(doc, "components.catalog."+key+".enabled", true)
			releaseName, _ := environment.GetPath(doc, "components.catalog."+key+".release_name")
			namespace, _ := environment.GetPath(doc, "components.catalog."+key+".namespace")
			repairs, conflicts := interruptedDataServiceRepairs(doc, "", []helmListRelease{{Name: releaseName.(string), Namespace: namespace.(string), Status: "pending-install"}})
			if len(conflicts) != 0 || len(repairs) != 1 || repairs[0].Key != key || !repairs[0].FreshInstall {
				t.Fatalf("interrupted %s was not selected for safe repair: repairs=%#v conflicts=%#v", key, repairs, conflicts)
			}
		})
	}
}

func TestBytebaseBootstrapUsesNumericNonRootIdentity(t *testing.T) {
	payload, err := os.ReadFile("../../terraform/platform/charts/bytebase/templates/bootstrap-job.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, required := range []string{"runAsNonRoot: true", "runAsUser: 100", "runAsGroup: 101"} {
		if !strings.Contains(source, required) {
			t.Fatalf("Bytebase bootstrap security context is missing %q", required)
		}
	}
}

func TestBytebaseOptionalBootstrapDoesNotFailComponentInstall(t *testing.T) {
	payload, err := os.ReadFile("../../terraform/platform/charts/bytebase/templates/bootstrap-job.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, required := range []string{
		"activeDeadlineSeconds: 420",
		"管理员自动初始化未完成",
		"MySQL 自动接入未完成",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Bytebase best-effort bootstrap is missing %q", required)
		}
	}
	if strings.Contains(source, `while [ "$attempt" -le 90 ]`) {
		t.Fatal("Bytebase optional integration still blocks a component deployment for fifteen minutes")
	}
}

func TestFailedUntrackedObservabilityReleaseIsSafelyRepairedWithoutPVCDeletion(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.prometheus.enabled", true)
	environment.SetPath(doc, "components.catalog.prometheus.namespace", "demo-test-monitoring")
	repairs, conflicts := interruptedDataServiceRepairs(doc, "", []helmListRelease{{Name: "prometheus", Namespace: "demo-test-monitoring", Status: "failed"}})
	if len(conflicts) != 0 || len(repairs) != 1 || repairs[0].Key != "prometheus" {
		t.Fatalf("failed Prometheus release was not selected for repair: repairs=%#v conflicts=%#v", repairs, conflicts)
	}
	if repairs[0].FreshInstall {
		t.Fatal("observability recovery must preserve PVCs even when Helm reports a failed install")
	}
}

func TestFailedBundledLogStackReleaseIsSafelyRepairedWithoutPVCDeletion(t *testing.T) {
	for _, key := range []string{"efk_stack", "clickvisual_stack"} {
		t.Run(key, func(t *testing.T) {
			doc := environment.DefaultDocument("demo", "prod")
			environment.SetPath(doc, "components.catalog."+key+".enabled", true)
			releaseName, _ := environment.GetPath(doc, "components.catalog."+key+".release_name")
			namespace, _ := environment.GetPath(doc, "components.catalog."+key+".namespace")
			repairs, conflicts := interruptedDataServiceRepairs(doc, "", []helmListRelease{{Name: releaseName.(string), Namespace: namespace.(string), Status: "failed"}})
			if len(conflicts) != 0 || len(repairs) != 1 || repairs[0].Key != key {
				t.Fatalf("failed bundled log stack was not selected for repair: repairs=%#v conflicts=%#v", repairs, conflicts)
			}
			if repairs[0].FreshInstall {
				t.Fatal("bundled log stack recovery must always retain PVCs")
			}
		})
	}
}

func TestCustomObservabilityChartIsNeverAutomaticallyRemoved(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.loki.enabled", true)
	environment.SetPath(doc, "components.catalog.loki.chart", "custom-loki")
	repairs, conflicts := interruptedDataServiceRepairs(doc, "", []helmListRelease{{Name: "loki", Namespace: "monitoring", Status: "failed"}})
	if len(conflicts) != 0 || len(repairs) != 0 {
		t.Fatalf("custom Loki chart must be left alone: repairs=%#v conflicts=%#v", repairs, conflicts)
	}
}

func TestTrackedOrHealthyDataServiceIsNeverAutomaticallyRemoved(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.mysql.enabled", true)
	releases := []helmListRelease{{Name: "mysql", Namespace: "platform-server", Status: "deployed"}}

	repairs, conflicts := interruptedDataServiceRepairs(doc, `helm_release.catalog["mysql"]`, releases)
	if len(repairs) != 0 || len(conflicts) != 0 {
		t.Fatalf("tracked release must be left alone: repairs=%#v conflicts=%#v", repairs, conflicts)
	}

	repairs, conflicts = interruptedDataServiceRepairs(doc, "", releases)
	if len(repairs) != 0 || len(conflicts) != 1 || conflicts[0] != "platform-server/mysql" {
		t.Fatalf("healthy untracked release requires import: repairs=%#v conflicts=%#v", repairs, conflicts)
	}
}

func TestExternalOrNonRetainedDataServiceIsNeverAutomaticallyRemoved(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.mysql.enabled", true)
	environment.SetPath(doc, "components.catalog.mysql.builtin_chart", "")
	releases := []helmListRelease{{Name: "mysql", Namespace: "platform-server", Status: "failed"}}

	repairs, conflicts := interruptedDataServiceRepairs(doc, "", releases)
	if len(repairs) != 0 || len(conflicts) != 0 {
		t.Fatalf("external release must be left alone: repairs=%#v conflicts=%#v", repairs, conflicts)
	}

	environment.SetPath(doc, "components.catalog.mysql.builtin_chart", "data-service")
	environment.SetPath(doc, "components.catalog.mysql.values.storage.retainOnDelete", false)
	repairs, conflicts = interruptedDataServiceRepairs(doc, "", releases)
	if len(repairs) != 0 || len(conflicts) != 0 {
		t.Fatalf("non-retained storage must be left alone: repairs=%#v conflicts=%#v", repairs, conflicts)
	}
}

func TestHelmInventoryArgumentsSupportHelmThreeAndFour(t *testing.T) {
	arguments := strings.Join(helmInventoryArgs(), " ")
	if strings.Contains(arguments, " --all ") {
		t.Fatalf("Helm 4 does not support --all: %s", arguments)
	}
	for _, required := range []string{"--all-namespaces", "--deployed", "--failed", "--pending", "--output json"} {
		if !strings.Contains(arguments, required) {
			t.Fatalf("Helm inventory arguments are missing %q: %s", required, arguments)
		}
	}
}

func TestBundledLogStacksHonorPVCRemovalPolicy(t *testing.T) {
	for _, path := range []string{
		"../../terraform/platform/charts/clickvisual-stack/templates/kafka.yaml",
		"../../terraform/platform/charts/clickvisual-stack/templates/clickhouse.yaml",
		"../../terraform/platform/charts/clickvisual-stack/templates/mysql.yaml",
	} {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		template := string(payload)
		if !strings.Contains(template, "if ") || !strings.Contains(template, ".Values.storage.retainOnDelete") || !strings.Contains(template, "helm.sh/resource-policy: keep") {
			t.Fatalf("ClickVisual PVC template %s does not conditionally retain data", path)
		}
	}

	efkPayload, err := os.ReadFile("../../terraform/platform/charts/efk-stack/templates/elasticsearch.yaml")
	if err != nil {
		t.Fatal(err)
	}
	efkTemplate := string(efkPayload)
	if !strings.Contains(efkTemplate, `whenDeleted: {{ ternary "Retain" "Delete" .Values.elasticsearch.storage.retainOnDelete }}`) {
		t.Fatal("EFK StatefulSet does not map the UI retention choice to Kubernetes PVC deletion policy")
	}
	if !strings.Contains(efkTemplate, "- eswrapper") {
		t.Fatal("EFK Elasticsearch args do not preserve the official image startup command")
	}
	fluentdPayload, err := os.ReadFile("../../terraform/platform/charts/efk-stack/templates/fluentd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fluentdTemplate := string(fluentdPayload)
	if !strings.Contains(fluentdTemplate, "name: prepare-tmp") || !strings.Contains(fluentdTemplate, "chmod 1777 /tmp") {
		t.Fatal("EFK Fluentd does not prepare a Ruby-compatible temporary directory")
	}
}
