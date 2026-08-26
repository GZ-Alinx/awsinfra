package componentcatalog

import (
	"context"
	"errors"
	"os"
	"testing"
)

type memoryStore struct{ items map[string]Component }

func (s *memoryStore) ListHelmComponents(context.Context) ([]Component, error) {
	result := make([]Component, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}
	return result, nil
}
func (s *memoryStore) GetHelmComponent(_ context.Context, key string) (Component, error) {
	item, ok := s.items[key]
	if !ok {
		return Component{}, os.ErrNotExist
	}
	return item, nil
}
func (s *memoryStore) SaveHelmComponent(_ context.Context, item Component) error {
	if s.items == nil {
		s.items = map[string]Component{}
	}
	s.items[item.Key] = item
	return nil
}
func (s *memoryStore) DeleteHelmComponent(_ context.Context, key string) error {
	delete(s.items, key)
	return nil
}

func TestSaveValidatesAndParsesValues(t *testing.T) {
	service := &Service{store: &memoryStore{}, allowPrivate: true}
	item, err := service.Save(context.Background(), Component{
		Key: "grafana-tempo", DisplayName: "Grafana Tempo", Category: "日志",
		Repository: "https://grafana.github.io/helm-charts", Chart: "tempo",
		DefaultNamespace: "monitoring", ReplicaPaths: []string{"replicaCount"}, ValuesYAML: "replicaCount: 2\nservice:\n  port: 3200\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Values["replicaCount"] != 2 || item.Values["service"].(map[string]any)["port"] != 3200 || len(item.ReplicaPaths) != 1 {
		t.Fatalf("unexpected parsed values: %#v", item.Values)
	}
}

func TestSaveRejectsUnsafeRepositoryAndInvalidYAML(t *testing.T) {
	service := &Service{store: &memoryStore{}, allowPrivate: true}
	base := Component{Key: "tempo", DisplayName: "Tempo", Category: "日志", Chart: "tempo", DefaultNamespace: "monitoring", ValuesYAML: "{}"}
	base.Repository = "file:///tmp/charts"
	if _, err := service.Save(context.Background(), base); !errors.Is(err, ErrInvalidComponent) {
		t.Fatalf("unsafe repository error = %v", err)
	}
	base.Repository = "https://grafana.github.io/helm-charts"
	base.ValuesYAML = "broken: ["
	if _, err := service.Save(context.Background(), base); !errors.Is(err, ErrInvalidComponent) {
		t.Fatalf("invalid YAML error = %v", err)
	}
}

func TestSaveRejectsInlineSecretsAndReservedKeys(t *testing.T) {
	service := &Service{store: &memoryStore{}, allowPrivate: true, reserved: map[string]bool{"etcd": true}}
	component := Component{
		Key: "tempo", DisplayName: "Tempo", Category: "日志", Repository: "https://example.com/charts",
		Chart: "tempo", DefaultNamespace: "monitoring", ValuesYAML: "adminPassword: do-not-store\n",
	}
	if _, err := service.Save(context.Background(), component); !errors.Is(err, ErrInlineSecret) {
		t.Fatalf("inline secret error = %v", err)
	}
	component.Key = "etcd"
	component.ValuesYAML = "{}\n"
	if _, err := service.Save(context.Background(), component); !errors.Is(err, ErrReservedComponent) {
		t.Fatalf("reserved key error = %v", err)
	}
}

func TestRepositoryHostBlocksPrivateNetworks(t *testing.T) {
	service := &Service{}
	for _, repository := range []string{"http://127.0.0.1/charts", "https://10.0.0.1/charts", "oci://localhost/charts"} {
		if err := service.validateRepositoryHost(context.Background(), repository); !errors.Is(err, ErrInvalidComponent) {
			t.Fatalf("repository %q was not blocked: %v", repository, err)
		}
	}
}
