package secrets

import (
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyLifecycle(t *testing.T) {
	keyring.MockInit() // in-memory backend — never touches the real keychain
	if HasAPIKey("openai") {
		t.Fatal("unexpected key present")
	}
	if err := SetAPIKey("openai", "sk-abc"); err != nil {
		t.Fatal(err)
	}
	if !HasAPIKey("openai") {
		t.Fatal("key not stored")
	}
	got, err := GetAPIKey("openai")
	if err != nil || got != "sk-abc" {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := DeleteAPIKey("openai"); err != nil {
		t.Fatal(err)
	}
	if HasAPIKey("openai") {
		t.Fatal("key not deleted")
	}
	if err := DeleteAPIKey("openai"); err != nil {
		t.Fatal("double delete must be a no-op, got", err)
	}
}
