package httpapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/GZ-Alinx/awsinfra/internal/environment"
)

func removedNamespaceNames(current, next environment.Document) []string {
	currentNamespaces, _ := current["namespaces"].(map[string]any)
	nextNamespaces, _ := next["namespaces"].(map[string]any)
	removed := make([]string, 0)
	for name := range currentNamespaces {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := nextNamespaces[name]; !exists {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	return removed
}

func namespaceRemovalError(names []string) error {
	return fmt.Errorf(
		"Namespace 永久删除保护已拦截配置保存：%s。组件安装、更新、卸载和配置保存均不允许删除 Namespace；请只卸载对应组件。只有销毁整套平台自建 EKS 时，Namespace 才会随集群一起消失",
		strings.Join(names, "、"),
	)
}
