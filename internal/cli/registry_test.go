package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/registryauth"
)

type memoryCredentials struct {
	credential registryauth.Credential
	err        error
}

func (s *memoryCredentials) Get(string) (registryauth.Credential, error) { return s.credential, s.err }
func (s *memoryCredentials) Put(c registryauth.Credential) error {
	if s.err == nil {
		s.credential = c
	}
	return s.err
}
func (s *memoryCredentials) Delete(registry string) error {
	if s.err == nil && s.credential.Registry == registry {
		s.credential = registryauth.Credential{}
	}
	return s.err
}

func TestLoginLogoutStoreOnlyAndFailurePreservation(t *testing.T) {
	r, err := Parse([]string{"login", "-u", "alice", "--password-stdin", "GHCR.IO"})
	if err != nil || r.Reference != "ghcr.io" {
		t.Fatalf("parse: %+v %v", r, err)
	}
	store := &memoryCredentials{}
	var output bytes.Buffer
	if err := executeRegistryCommand(r, strings.NewReader("test-token\n"), &output, store); err != nil {
		t.Fatal(err)
	}
	if store.credential.Password != "test-token" || strings.Contains(output.String(), "test-token") || !strings.Contains(output.String(), "access is checked on pull") {
		t.Fatal("invalid saved credential or success output")
	}
	prior := store.credential
	if err := executeRegistryCommand(r, strings.NewReader("invalid\nmulti"), &output, store); err == nil || store.credential != prior {
		t.Fatal("invalid input replaced entry")
	}
	store.err = errors.New("Keychain locked")
	if err := executeRegistryCommand(r, strings.NewReader("new-token"), &output, store); err == nil || store.credential != prior {
		t.Fatal("storage failure ignored")
	}
	store.err = nil
	logout, _ := Parse([]string{"logout", "ghcr.io"})
	if err := executeRegistryCommand(logout, nil, &output, store); err != nil || store.credential.Password != "" {
		t.Fatal("logout failed")
	}
	for _, args := range [][]string{{"login"}, {"login", "-u", "alice"}, {"login", "--password-stdin"}, {"login", "--password", "secret"}, {"logout", "one.io", "two.io"}, {"logout", "https://ghcr.io"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
