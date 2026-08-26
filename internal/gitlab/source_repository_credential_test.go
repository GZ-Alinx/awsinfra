package gitlab

import (
	"strings"
	"testing"
)

func TestDeliveryCredentialKeyIsolatedByConnection(t *testing.T) {
	testKey := deliveryCredentialKey("managed-test")
	prodKey := deliveryCredentialKey("managed-prod")
	if testKey == prodKey {
		t.Fatalf("delivery credential keys must be isolated: %q", testKey)
	}
	if testKey != "gitlab-delivery-read-managed-test" {
		t.Fatalf("unexpected test key: %q", testKey)
	}
	if prodKey != "gitlab-delivery-read-managed-prod" {
		t.Fatalf("unexpected prod key: %q", prodKey)
	}
}

func TestDeliveryCredentialKeyIsolatedByEnvironment(t *testing.T) {
	testKey := deliveryCredentialKeyForEnvironment("jenkins", "test")
	prodKey := deliveryCredentialKeyForEnvironment("jenkins", "prod")
	if testKey == prodKey || testKey != "gitlab-delivery-read-jenkins-test" || prodKey != "gitlab-delivery-read-jenkins-prod" {
		t.Fatalf("environment credential keys are not isolated: %q / %q", testKey, prodKey)
	}
}

func TestDeliveryCredentialKeyLengthAndDeterminism(t *testing.T) {
	connection := strings.Repeat("production-", 12)
	first := deliveryCredentialKey(connection)
	second := deliveryCredentialKey(connection)
	if first != second {
		t.Fatalf("credential key must be deterministic: %q != %q", first, second)
	}
	if len(first) > 63 {
		t.Fatalf("credential key exceeds Jenkins/platform limit: %d", len(first))
	}
	if !strings.HasPrefix(first, "gitlab-delivery-read-") {
		t.Fatalf("credential key lost its readable prefix: %q", first)
	}
}
