package httpapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GZ-Alinx/awsinfra/internal/environment"
)

func TestRemovedNamespaceNamesIsSortedAndOnlyIncludesRemovedEntries(t *testing.T) {
	current := environment.Document{"namespaces": map[string]any{
		"zeta":  map[string]any{},
		"alpha": map[string]any{},
		"keep":  map[string]any{},
	}}
	next := environment.Document{"namespaces": map[string]any{
		"keep": map[string]any{},
		"new":  map[string]any{},
	}}
	if got, want := removedNamespaceNames(current, next), []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed namespaces = %#v, want %#v", got, want)
	}
}

func TestNamespaceRemovalIsAlwaysRejected(t *testing.T) {
	message := namespaceRemovalError([]string{"alpha", "zeta"}).Error()
	for _, required := range []string{"永久删除保护", "alpha、zeta", "组件安装、更新、卸载", "只卸载对应组件"} {
		if !strings.Contains(message, required) {
			t.Fatalf("namespace removal error is missing %q: %s", required, message)
		}
	}
}
