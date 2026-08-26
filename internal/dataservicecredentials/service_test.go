package dataservicecredentials

import (
	"context"
	"encoding/base64"
	"os"
	"testing"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
)

type memoryStore struct{ records map[string]StoredCredential }

func (m *memoryStore) ListDataServiceCredentials(_ context.Context, project, environment string) ([]StoredCredential, error) {
	result := make([]StoredCredential, 0)
	for _, record := range m.records {
		if record.ProjectKey == project && record.EnvironmentKey == environment {
			result = append(result, record)
		}
	}
	return result, nil
}
func (m *memoryStore) GetDataServiceCredential(_ context.Context, project, environment, service string) (StoredCredential, error) {
	record, ok := m.records[project+"/"+environment+"/"+service]
	if !ok {
		return StoredCredential{}, os.ErrNotExist
	}
	return record, nil
}
func (m *memoryStore) SaveDataServiceCredential(_ context.Context, record StoredCredential) error {
	m.records[record.ProjectKey+"/"+record.EnvironmentKey+"/"+record.ServiceKey] = record
	return nil
}
func (m *memoryStore) DeleteDataServiceCredential(_ context.Context, project, environment, service string) error {
	delete(m.records, project+"/"+environment+"/"+service)
	return nil
}

func TestPasswordIsEncryptedAndNeverReturnedByList(t *testing.T) {
	key := make([]byte, 32)
	config := &appconfig.Config{Security: appconfig.SecurityConfig{CredentialKeyEnv: "TEST_KEY"}}
	t.Setenv("TEST_KEY", base64.StdEncoding.EncodeToString(key))
	store := &memoryStore{records: map[string]StoredCredential{}}
	service, err := New(config, store)
	if err != nil {
		t.Fatal(err)
	}
	const password = "SafePass-123!"
	if _, err := service.Save(context.Background(), "demo", "test", "rds", "admin", Input{Username: "ops_admin", Password: password}); err != nil {
		t.Fatal(err)
	}
	record := store.records["demo/test/rds"]
	if record.PasswordCipher == password || record.PasswordCipher == "" {
		t.Fatalf("password was not encrypted: %q", record.PasswordCipher)
	}
	items, err := service.List(context.Background(), "demo", "test")
	if err != nil || len(items) != 1 || !items[0].Configured {
		t.Fatalf("unexpected public list: %#v, %v", items, err)
	}
	materials, err := service.Materials(context.Background(), "demo", "test")
	if err != nil || materials["rds"].Password != password {
		t.Fatalf("credential did not decrypt for deployment: %#v, %v", materials, err)
	}
}

func TestPasswordRejectsAWSForbiddenCharacters(t *testing.T) {
	for _, value := range []string{"short", "Bad/Password1", "Bad@Password1", "Bad Password1", `Bad"Password1`} {
		if validPassword(value) {
			t.Fatalf("password %q should be rejected", value)
		}
	}
}
