package environment

import (
	"reflect"
	"sort"
	"strconv"
)

// NodeGroupSchedulingPlan contains the immutable part of a managed EKS node
// group plan. Capacity values are deliberately excluded so operators can keep
// scaling an existing group without changing its placement contract.
type NodeGroupSchedulingPlan struct {
	Enabled bool
	Groups  map[string]NodeGroupPlacement
}

type NodeGroupPlacement struct {
	InstanceTypes     []string
	AvailabilityZones []string
	CapacityType      string
	SubnetType        string
	AMIType           string
	DiskSize          int
	Labels            map[string]string
	Taints            []NodeGroupTaint
}

type NodeGroupTaint struct {
	Key    string
	Value  string
	Effect string
}

// NodeGroupSchedulingChanges describes changes to the immutable placement
// contract of managed node groups. Capacity is intentionally excluded: an
// operator may always adjust min/max capacity, while an existing node group's
// instance/network/scheduling contract remains protected.
type NodeGroupSchedulingChanges struct {
	EnabledChanged bool
	Added          []string
	Removed        []string
	Modified       []string
}

func WorkloadSchedulingEnabled(doc Document) bool {
	eks, ok := mapValue(doc["eks"])
	if !ok {
		return false
	}
	config, ok := mapValue(eks["workload_scheduling"])
	return ok && boolValue(config["enabled"])
}

// NormalizeManagedNodeGroupTaints enforces the platform scheduling contract:
// only business workload nodes use a NoSchedule taint. Gateway and platform
// workloads are still pinned with workload-class labels and selectors, while
// keeping those nodes available to EKS system workloads when necessary.
func NormalizeManagedNodeGroupTaints(doc Document) {
	eks, ok := mapValue(doc["eks"])
	if !ok {
		return
	}
	groups, ok := mapValue(eks["node_groups"])
	if !ok {
		return
	}
	for _, raw := range groups {
		group, valid := mapValue(raw)
		if !valid {
			continue
		}
		labels, _ := mapValue(group["labels"])
		role := stringValue(labels["workload-class"])
		if role == "application" {
			group["taints"] = []any{map[string]any{
				"key": "workload-class", "value": "application", "effect": "NO_SCHEDULE",
			}}
			continue
		}
		group["taints"] = []any{}
	}
}

func SchedulingPlan(doc Document) NodeGroupSchedulingPlan {
	plan := NodeGroupSchedulingPlan{Enabled: WorkloadSchedulingEnabled(doc), Groups: map[string]NodeGroupPlacement{}}
	eks, ok := mapValue(doc["eks"])
	if !ok {
		return plan
	}
	groups, ok := mapValue(eks["node_groups"])
	if !ok {
		return plan
	}
	for name, raw := range groups {
		group, valid := mapValue(raw)
		if !valid {
			continue
		}
		placement := NodeGroupPlacement{
			InstanceTypes:     stringList(group["instance_types"]),
			AvailabilityZones: stringList(group["availability_zones"]),
			CapacityType:      stringValue(group["capacity_type"]),
			SubnetType:        stringValue(group["subnet_type"]),
			AMIType:           stringValue(group["ami_type"]),
			DiskSize:          intValue(group["disk_size"]),
			Labels:            map[string]string{},
		}
		if placement.CapacityType == "" {
			placement.CapacityType = "ON_DEMAND"
		}
		if placement.AMIType == "" {
			placement.AMIType = "AL2023_x86_64_STANDARD"
		}
		sort.Strings(placement.InstanceTypes)
		sort.Strings(placement.AvailabilityZones)
		if labels, valid := mapValue(group["labels"]); valid {
			for key, value := range labels {
				placement.Labels[key] = stringValue(value)
			}
		}
		if taints, valid := group["taints"].([]any); valid {
			for _, rawTaint := range taints {
				if taint, valid := mapValue(rawTaint); valid {
					placement.Taints = append(placement.Taints, NodeGroupTaint{
						Key: stringValue(taint["key"]), Value: stringValue(taint["value"]), Effect: stringValue(taint["effect"]),
					})
				}
			}
		}
		sort.Slice(placement.Taints, func(i, j int) bool {
			left, right := placement.Taints[i], placement.Taints[j]
			if left.Key != right.Key {
				return left.Key < right.Key
			}
			if left.Value != right.Value {
				return left.Value < right.Value
			}
			return left.Effect < right.Effect
		})
		plan.Groups[name] = placement
	}
	return plan
}

func SameSchedulingPlan(current, next Document) bool {
	return reflect.DeepEqual(SchedulingPlan(current), SchedulingPlan(next))
}

// CompareSchedulingPlans allows callers to implement append-only updates for
// an existing EKS cluster: new node groups are safe to create, but removing or
// mutating a provisioned group could evict workloads or destroy capacity.
func CompareSchedulingPlans(current, next Document) NodeGroupSchedulingChanges {
	currentPlan, nextPlan := SchedulingPlan(current), SchedulingPlan(next)
	changes := NodeGroupSchedulingChanges{EnabledChanged: currentPlan.Enabled != nextPlan.Enabled}
	for name, currentGroup := range currentPlan.Groups {
		nextGroup, exists := nextPlan.Groups[name]
		if !exists {
			changes.Removed = append(changes.Removed, name)
			continue
		}
		if !reflect.DeepEqual(currentGroup, nextGroup) {
			changes.Modified = append(changes.Modified, name)
		}
	}
	for name := range nextPlan.Groups {
		if _, exists := currentPlan.Groups[name]; !exists {
			changes.Added = append(changes.Added, name)
		}
	}
	sort.Strings(changes.Added)
	sort.Strings(changes.Removed)
	sort.Strings(changes.Modified)
	return changes
}

// ConfigureNewEnvironmentScheduling is called only while a new environment
// record is being created. It upgrades a cloned legacy layout into the new
// application/platform split without changing an existing environment.
func ConfigureNewEnvironmentScheduling(doc Document) {
	defer NormalizeManagedNodeGroupTaints(doc)
	eks, ok := mapValue(doc["eks"])
	if !ok {
		return
	}
	if IsExistingEKS(doc) {
		eks["workload_scheduling"] = map[string]any{"enabled": false}
		return
	}
	eks["workload_scheduling"] = map[string]any{"enabled": true}
	groups, ok := mapValue(eks["node_groups"])
	if !ok {
		return
	}
	roles := map[string]bool{}
	names := make([]string, 0, len(groups))
	for name, raw := range groups {
		names = append(names, name)
		if group, valid := mapValue(raw); valid {
			if labels, valid := mapValue(group["labels"]); valid {
				roles[stringValue(labels["workload-class"])] = true
			}
		}
	}
	sort.Strings(names)
	if !roles["application"] && len(names) > 0 {
		if group, valid := mapValue(groups[names[0]]); valid {
			labels, _ := mapValue(group["labels"])
			if labels == nil {
				labels = map[string]any{}
				group["labels"] = labels
			}
			labels["workload-class"] = "application"
			roles["application"] = true
		}
	}
	if roles["platform"] {
		return
	}
	defaults := DefaultDocument(stringValue(doc["project"]), stringValue(doc["environment"]))
	if region := stringValue(doc["region"]); region != "" {
		_ = ConfigureRegion(defaults, region)
	}
	defaultGroups, _ := mapValue(defaults["eks"].(map[string]any)["node_groups"])
	platform, _ := mapValue(defaultGroups["platform-ops"])
	name := "platform-ops"
	for suffix := 2; groups[name] != nil; suffix++ {
		name = "platform-ops-" + strconv.Itoa(suffix)
	}
	groups[name] = platform
}
