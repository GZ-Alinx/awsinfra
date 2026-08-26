package status

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ManagedStorage describes one PVC owned by a platform-managed stateful stack.
// Kubernetes object names are returned for display only; mutation endpoints
// rediscover them from trusted labels and never accept them from the browser.
type ManagedStorage struct {
	Component      string `json:"component"`
	Namespace      string `json:"namespace"`
	PVCName        string `json:"pvc_name"`
	Requested      string `json:"requested"`
	Capacity       string `json:"capacity"`
	Phase          string `json:"phase"`
	StorageClass   string `json:"storage_class"`
	AllowExpansion bool   `json:"allow_expansion"`
	WorkloadKind   string `json:"workload_kind"`
	WorkloadName   string `json:"workload_name"`
	VolumeName     string `json:"volume_name"`
	Active         bool   `json:"active"`
	Retained       bool   `json:"retained"`
	Project        string `json:"-"`
	Environment    string `json:"-"`
}

type ManagedStorageReport struct {
	ObservedAt time.Time        `json:"observed_at"`
	Items      []ManagedStorage `json:"items"`
}

// ListClickVisualStorage performs a focused live query. It deliberately does
// not run the full environment status collector, keeping the storage panel
// responsive and avoiding unrelated AWS/Helm calls.
func (s *Service) ListClickVisualStorage(ctx context.Context, name string) (*ManagedStorageReport, error) {
	return s.listManagedStorage(ctx, name, "clickvisual", "ClickVisual 日志平台")
}

// ListOpenTelemetryStorage reads the live WAL PVCs owned by the Collector.
// The Collector is not a long-term telemetry backend: these volumes only hold
// file_storage sending queues while an exporter destination is unavailable.
func (s *Service) ListOpenTelemetryStorage(ctx context.Context, name string) (*ManagedStorageReport, error) {
	return s.listManagedStorage(ctx, name, "opentelemetry", "OpenTelemetry Collector")
}

func (s *Service) listManagedStorage(ctx context.Context, name, stack, displayName string) (*ManagedStorageReport, error) {
	commandContext, doc, kubeconfig, err := s.kubernetesContext(ctx, name)
	if err != nil {
		return nil, err
	}
	defer os.Remove(kubeconfig)

	allowed, err := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{
		"auth", "can-i", "list", "persistentvolumeclaims", "--all-namespaces",
	}, kubeconfig)
	if err != nil || !strings.EqualFold(strings.TrimSpace(string(allowed)), "yes") {
		return nil, errors.New("当前项目 AWS 身份没有读取 PVC 的 Kubernetes 权限")
	}
	pvcPayload, err := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{
		"get", "persistentvolumeclaims", "-A", "-l", "ops-deploy.io/stack=" + stack, "-o", "json",
	}, kubeconfig)
	if err != nil {
		return nil, errors.New("读取 " + displayName + " PVC 失败")
	}
	classPayload, err := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{
		"get", "storageclasses.storage.k8s.io", "-o", "json",
	}, kubeconfig)
	if err != nil {
		return nil, errors.New("读取 EKS StorageClass 失败")
	}
	workloadPayload, err := s.capture(commandContext, "", s.config.Tools.Kubectl, []string{
		"get", "statefulsets.apps", "-A", "-l", "ops-deploy.io/stack=" + stack, "-o", "json",
	}, kubeconfig)
	if err != nil {
		return nil, errors.New("读取 " + displayName + " 工作负载失败")
	}
	items, err := decodeManagedStorage(pvcPayload, classPayload, workloadPayload)
	if err != nil {
		return nil, errors.New("EKS 返回的 " + displayName + " 存储数据格式无效")
	}
	project, environmentName := stringAt(doc, "project"), stringAt(doc, "environment")
	filtered := items[:0]
	for _, item := range items {
		if item.Project == project && item.Environment == environmentName {
			filtered = append(filtered, item)
		}
	}
	return &ManagedStorageReport{ObservedAt: time.Now().UTC(), Items: filtered}, nil
}

func decodeManagedStorage(pvcPayload, classPayload, workloadPayload []byte) ([]ManagedStorage, error) {
	var classes struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			AllowVolumeExpansion bool `json:"allowVolumeExpansion"`
		} `json:"items"`
	}
	if err := json.Unmarshal(classPayload, &classes); err != nil {
		return nil, err
	}
	expansion := make(map[string]bool, len(classes.Items))
	for _, item := range classes.Items {
		expansion[item.Metadata.Name] = item.AllowVolumeExpansion
	}

	var workloads struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Replicas             *int `json:"replicas"`
				VolumeClaimTemplates []struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
				} `json:"volumeClaimTemplates"`
				Template struct {
					Spec struct {
						Volumes []struct {
							Name                  string `json:"name"`
							PersistentVolumeClaim *struct {
								ClaimName string `json:"claimName"`
							} `json:"persistentVolumeClaim"`
						} `json:"volumes"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(workloadPayload, &workloads); err != nil {
		return nil, err
	}
	activeClaims := make(map[string]bool)
	for _, workload := range workloads.Items {
		replicas := 1
		if workload.Spec.Replicas != nil {
			replicas = *workload.Spec.Replicas
		}
		for _, claimTemplate := range workload.Spec.VolumeClaimTemplates {
			for ordinal := 0; ordinal < replicas; ordinal++ {
				claimName := claimTemplate.Metadata.Name + "-" + workload.Metadata.Name + "-" + strconv.Itoa(ordinal)
				activeClaims[workload.Metadata.Namespace+"\x00"+claimName] = true
			}
		}
		for _, volume := range workload.Spec.Template.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				activeClaims[workload.Metadata.Namespace+"\x00"+volume.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}

	var pvcs struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Namespace   string            `json:"namespace"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				StorageClassName string `json:"storageClassName"`
				Resources        struct {
					Requests map[string]string `json:"requests"`
				} `json:"resources"`
			} `json:"spec"`
			Status struct {
				Phase    string            `json:"phase"`
				Capacity map[string]string `json:"capacity"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(pvcPayload, &pvcs); err != nil {
		return nil, err
	}
	result := make([]ManagedStorage, 0, len(pvcs.Items))
	for _, item := range pvcs.Items {
		active := activeClaims[item.Metadata.Namespace+"\x00"+item.Metadata.Name]
		result = append(result, ManagedStorage{
			Component:      item.Metadata.Labels["ops-deploy.io/component"],
			Namespace:      item.Metadata.Namespace,
			PVCName:        item.Metadata.Name,
			Requested:      item.Spec.Resources.Requests["storage"],
			Capacity:       item.Status.Capacity["storage"],
			Phase:          item.Status.Phase,
			StorageClass:   item.Spec.StorageClassName,
			AllowExpansion: expansion[item.Spec.StorageClassName],
			WorkloadKind:   item.Metadata.Labels["ops-deploy.io/workload-kind"],
			WorkloadName:   item.Metadata.Labels["ops-deploy.io/workload-name"],
			VolumeName:     item.Metadata.Labels["ops-deploy.io/volume-name"],
			Active:         active,
			Retained:       !active || item.Metadata.Annotations["ops-deploy.io/retained-after-resize"] == "true",
			Project:        item.Metadata.Labels["ops-deploy.io/project"],
			Environment:    item.Metadata.Labels["ops-deploy.io/environment"],
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Component == result[j].Component {
			if result[i].Active != result[j].Active {
				return result[i].Active
			}
			return result[i].PVCName < result[j].PVCName
		}
		return result[i].Component < result[j].Component
	})
	return result, nil
}
