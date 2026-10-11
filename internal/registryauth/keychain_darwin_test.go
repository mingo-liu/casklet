//go:build darwin

package registryauth

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestKeychainStatusPreservesMissingAndSanitizesErrors(t *testing.T) {
	if !errors.Is(keychainStatus("read", -25300), ErrNotFound) {
		t.Fatal("missing entry lost")
	}
	if err := keychainStatus("read", -25308); err == nil {
		t.Fatal("locked entry ignored")
	}
	if err := keychainStatus("write", 0); err != nil {
		t.Fatal(err)
	}
}

func TestNativeKeychainRoundTrip(t *testing.T) {
	if os.Getenv("CASKLET_KEYCHAIN_TEST") != "1" {
		t.Skip("set CASKLET_KEYCHAIN_TEST=1 to use a unique temporary Keychain entry")
	}
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	registry := fmt.Sprintf("casklet-test-%d-%d.invalid", os.Getpid(), time.Now().UnixNano())
	// Never update/delete a user's existing account, even with a name collision.
	if _, err := store.Get(registry); !errors.Is(err, ErrNotFound) {
		t.Fatalf("temporary account not absent: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Delete(registry); err != nil {
			t.Error(err)
		}
	})
	c := Credential{Registry: registry, Username: "test-user", Password: "temporary test token ' \" \\ spaces"}
	if err := store.Put(c); err != nil {
		t.Fatal(err)
	}
	actual, err := store.Get(registry)
	if err != nil || actual != c {
		t.Fatalf("Keychain round trip failed: %v", err)
	}
	c.Username, c.Password = "updated-user", "updated temporary token"
	if err := store.Put(c); err != nil {
		t.Fatal(err)
	}
	actual, err = store.Get(registry)
	if err != nil || actual != c {
		t.Fatalf("Keychain update failed: %v", err)
	}
	if err := store.Delete(registry); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(registry); !errors.Is(err, ErrNotFound) {
		t.Fatalf("entry remained: %v", err)
	}
	if err := store.Delete(registry); err != nil {
		t.Fatal(err)
	}
}
