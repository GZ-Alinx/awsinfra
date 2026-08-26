package environment

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	TargetManaged     = "managed"
	TargetExistingEKS = "existing_eks"
)

var (
	eksClusterNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)
	namespaceUnsafe       = regexp.MustCompile(`[^a-z0-9-]+`)
)

// TargetType returns the environment's infrastructure ownership model. Older
// documents default to managed so the migration cannot accidentally detach
// resources that are already owned by this platform.
func TargetType(doc Document) string {
	if target, ok := mapValue(doc["deployment_target"]); ok {
		if value := stringValue(target["type"]); value != "" {
			return value
		}
	}
	return TargetManaged
}

func IsExistingEKS(doc Document) bool { return TargetType(doc) == TargetExistingEKS }

func ClusterName(doc Document) string {
	if target, ok := mapValue(doc["deployment_target"]); ok && TargetType(doc) == TargetExistingEKS {
		if name := stringValue(target["cluster_name"]); name != "" {
			return name
		}
	}
	return fmt.Sprintf("%s-%s-eks", stringValue(doc["project"]), stringValue(doc["environment"]))
}

func ManageClusterAddons(doc Document) bool {
	return !IsExistingEKS(doc)
}

// ConfigureTarget is used while creating an environment. Existing-cluster
// targets receive project-scoped namespaces so two projects can safely use the
// same EKS cluster without sharing Helm release ownership.
func ConfigureTarget(doc Document, targetType string) error {
	targetType = strings.TrimSpace(targetType)
	if targetType == "" {
		targetType = TargetManaged
	}
	if targetType != TargetManaged && targetType != TargetExistingEKS {
		return fmt.Errorf("deployment target type must be managed or existing_eks")
	}
	prefix := namespacePrefix(stringValue(doc["project"]), stringValue(doc["environment"]))
	doc["deployment_target"] = map[string]any{
		"type": targetType, "cluster_name": "", "manage_addons": targetType == TargetManaged,
		"namespace_prefix": prefix,
	}
	if targetType != TargetExistingEKS {
		return nil
	}
	if eks, ok := mapValue(doc["eks"]); ok {
		eks["workload_scheduling"] = map[string]any{"enabled": false}
	}

	namespaceMap := make(map[string]string)
	mapNamespace := func(value string) string {
		value = strings.TrimSpace(value)
		if value == "" {
			return ""
		}
		if mapped := namespaceMap[value]; mapped != "" {
			return mapped
		}
		suffix := value
		switch value {
		case "platform-server":
			suffix = "platform"
		case "monitoring":
			suffix = "monitoring"
		}
		mapped := prefixedNamespace(prefix, suffix)
		namespaceMap[value] = mapped
		return mapped
	}

	newNamespaces := make(map[string]any)
	if namespaces, ok := mapValue(doc["namespaces"]); ok {
		for name := range namespaces {
			newNamespaces[mapNamespace(name)] = map[string]any{}
		}
	}
	if components, ok := mapValue(doc["components"]); ok {
		for _, key := range []string{"consul", "etcd"} {
			if config, valid := mapValue(components[key]); valid {
				config["namespace"] = mapNamespace(stringValue(config["namespace"]))
				newNamespaces[stringValue(config["namespace"])] = map[string]any{}
			}
		}
		if catalog, valid := mapValue(components["catalog"]); valid {
			for _, raw := range catalog {
				if config, valid := mapValue(raw); valid {
					config["namespace"] = mapNamespace(stringValue(config["namespace"]))
					newNamespaces[stringValue(config["namespace"])] = map[string]any{}
				}
			}
		}
	}
	if alerting, ok := mapValue(doc["alerting"]); ok {
		alerting["namespace"] = mapNamespace(stringValue(alerting["namespace"]))
		newNamespaces[stringValue(alerting["namespace"])] = map[string]any{}
	}
	remapListNamespaces(doc["domains"], mapNamespace, newNamespaces)
	if tlsConfig, ok := mapValue(doc["tls"]); ok {
		remapListNamespaces(tlsConfig["certificates"], mapNamespace, newNamespaces)
	}
	delete(newNamespaces, "")
	doc["namespaces"] = newNamespaces
	return nil
}

func ValidateTarget(doc Document) error {
	targetType := TargetType(doc)
	if targetType != TargetManaged && targetType != TargetExistingEKS {
		return errors.New("deployment_target.type must be managed or existing_eks")
	}
	if targetType == TargetExistingEKS {
		target, _ := mapValue(doc["deployment_target"])
		name := stringValue(target["cluster_name"])
		if !eksClusterNamePattern.MatchString(name) {
			return errors.New("deployment_target.cluster_name must be a valid existing EKS cluster name")
		}
	}
	return nil
}

func remapListNamespaces(value any, mapper func(string) string, namespaces map[string]any) {
	items, ok := value.([]any)
	if !ok {
		return
	}
	for _, raw := range items {
		if item, valid := mapValue(raw); valid {
			item["namespace"] = mapper(stringValue(item["namespace"]))
			if namespace := stringValue(item["namespace"]); namespace != "" {
				namespaces[namespace] = map[string]any{}
			}
		}
	}
}

func namespacePrefix(project, environmentName string) string {
	return prefixedNamespace(project, environmentName)
}

func prefixedNamespace(prefix, suffix string) string {
	value := strings.Trim(strings.ToLower(prefix+"-"+suffix), "-")
	value = namespaceUnsafe.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if len(value) > 63 {
		value = strings.TrimRight(value[:63], "-")
	}
	return value
}
