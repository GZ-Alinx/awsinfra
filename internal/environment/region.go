package environment

import (
	"errors"
	"strings"
)

// ConfigureRegion updates the Region and all Region-qualified availability
// zone references together. Keeping this atomic prevents a newly created
// environment from carrying ap-south-1 subnet keys into another Region.
func ConfigureRegion(doc Document, region string) error {
	region = strings.TrimSpace(region)
	if !regionPattern.MatchString(region) {
		return errors.New("region must be a valid AWS region code")
	}
	doc["region"] = region
	network, ok := mapValue(doc["network"])
	if !ok {
		return errors.New("network must be an object")
	}
	oldZones := stringList(network["availability_zones"])
	publicSubnets, _ := mapValue(network["public_subnets"])
	privateSubnets, _ := mapValue(network["private_subnets"])
	publicCIDRs := subnetValues(oldZones, publicSubnets)
	privateCIDRs := subnetValues(oldZones, privateSubnets)

	zones := []any{region + "a", region + "b", region + "c"}
	network["availability_zones"] = zones
	network["workload_subnet_zones"] = append([]any(nil), zones...)
	network["data_subnet_zones"] = append([]any(nil), zones...)
	network["public_subnets"] = map[string]any{
		region + "a": valueOr(publicCIDRs, 0, "10.40.0.0/20"),
		region + "b": valueOr(publicCIDRs, 1, "10.40.16.0/20"),
		region + "c": valueOr(publicCIDRs, 2, "10.40.32.0/20"),
	}
	network["private_subnets"] = map[string]any{
		region + "a": valueOr(privateCIDRs, 0, "10.40.64.0/20"),
		region + "b": valueOr(privateCIDRs, 1, "10.40.80.0/20"),
		region + "c": valueOr(privateCIDRs, 2, "10.40.96.0/20"),
	}
	if eks, valid := mapValue(doc["eks"]); valid {
		if groups, valid := mapValue(eks["node_groups"]); valid {
			for _, raw := range groups {
				if group, valid := mapValue(raw); valid {
					group["availability_zones"] = append([]any(nil), zones...)
				}
			}
		}
	}
	return nil
}

func stringList(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text := stringValue(item); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func subnetValues(zones []string, subnets map[string]any) []string {
	result := make([]string, 0, len(zones))
	for _, zone := range zones {
		result = append(result, stringValue(subnets[zone]))
	}
	return result
}

func valueOr(values []string, index int, fallback string) string {
	if index < len(values) && values[index] != "" {
		return values[index]
	}
	return fallback
}
