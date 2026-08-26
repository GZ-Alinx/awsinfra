package status

import (
	"testing"
)

func TestDecodeManagedStorageMarksOnlyMountedClaimActive(t *testing.T) {
	pvcs := []byte(`{
	  "items": [
	    {
	      "metadata": {
	        "name": "clickvisual-clickhouse-data",
	        "namespace": "demo-test-logs-system",
	        "labels": {
	          "ops-deploy.io/component": "clickhouse",
	          "ops-deploy.io/project": "demo",
	          "ops-deploy.io/environment": "test",
	          "ops-deploy.io/workload-kind": "statefulset",
	          "ops-deploy.io/workload-name": "clickvisual-clickhouse",
	          "ops-deploy.io/volume-name": "data"
	        }
	      },
	      "spec": {"storageClassName": "gp3", "resources": {"requests": {"storage": "100Gi"}}},
	      "status": {"phase": "Bound", "capacity": {"storage": "100Gi"}}
	    },
	    {
	      "metadata": {
	        "name": "clickvisual-clickhouse-data-resize-old",
	        "namespace": "demo-test-logs-system",
	        "labels": {
	          "ops-deploy.io/component": "clickhouse",
	          "ops-deploy.io/project": "demo",
	          "ops-deploy.io/environment": "test",
	          "ops-deploy.io/workload-kind": "statefulset",
	          "ops-deploy.io/workload-name": "clickvisual-clickhouse",
	          "ops-deploy.io/volume-name": "data"
	        },
	        "annotations": {"ops-deploy.io/retained-after-resize": "true"}
	      },
	      "spec": {"storageClassName": "gp3", "resources": {"requests": {"storage": "50Gi"}}},
	      "status": {"phase": "Bound", "capacity": {"storage": "50Gi"}}
	    }
	  ]
	}`)
	classes := []byte(`{"items":[{"metadata":{"name":"gp3"},"allowVolumeExpansion":true}]}`)
	workloads := []byte(`{
	  "items": [{
	    "metadata": {"name": "clickvisual-clickhouse", "namespace": "demo-test-logs-system"},
	    "spec": {"template": {"spec": {"volumes": [{"name": "data", "persistentVolumeClaim": {"claimName": "clickvisual-clickhouse-data"}}]}}}
	  }]
	}`)
	items, err := decodeManagedStorage(pvcs, classes, workloads)
	if err != nil {
		t.Fatalf("decodeManagedStorage: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if !items[0].Active || items[0].PVCName != "clickvisual-clickhouse-data" {
		t.Fatalf("expected active claim first, got %#v", items[0])
	}
	if !items[0].AllowExpansion {
		t.Fatal("expected gp3 to allow expansion")
	}
	if items[1].Active || !items[1].Retained {
		t.Fatalf("expected second claim retained, got %#v", items[1])
	}
	if items[0].Project != "demo" || items[0].Environment != "test" {
		t.Fatalf("project scope labels were not decoded: %#v", items[0])
	}
}

func TestDecodeManagedStorageRejectsInvalidPayload(t *testing.T) {
	if _, err := decodeManagedStorage([]byte(`{`), []byte(`{"items":[]}`), []byte(`{"items":[]}`)); err == nil {
		t.Fatal("expected invalid PVC JSON to fail")
	}
}

func TestDecodeManagedStorageRecognizesStatefulSetClaimTemplates(t *testing.T) {
	pvcs := []byte(`{"items":[{"metadata":{"name":"collector-storage-opentelemetry-collector-0","namespace":"monitoring","labels":{"ops-deploy.io/component":"opentelemetry_collector","ops-deploy.io/project":"demo","ops-deploy.io/environment":"test","ops-deploy.io/workload-kind":"statefulset","ops-deploy.io/workload-name":"opentelemetry-collector","ops-deploy.io/volume-name":"collector-storage"}},"spec":{"storageClassName":"gp3","resources":{"requests":{"storage":"10Gi"}}},"status":{"phase":"Bound","capacity":{"storage":"10Gi"}}}]}`)
	classes := []byte(`{"items":[{"metadata":{"name":"gp3"},"allowVolumeExpansion":true}]}`)
	workloads := []byte(`{"items":[{"metadata":{"name":"opentelemetry-collector","namespace":"monitoring"},"spec":{"replicas":1,"volumeClaimTemplates":[{"metadata":{"name":"collector-storage"}}],"template":{"spec":{"volumes":[]}}}}]}`)

	items, err := decodeManagedStorage(pvcs, classes, workloads)
	if err != nil {
		t.Fatalf("decodeManagedStorage: %v", err)
	}
	if len(items) != 1 || !items[0].Active || items[0].Component != "opentelemetry_collector" {
		t.Fatalf("StatefulSet-generated Collector claim was not recognized: %#v", items)
	}
}
