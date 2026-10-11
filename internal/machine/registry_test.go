package machine

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/registryauth"
)

type credentialResponse struct {
	bytes.Buffer
	closed bool
}

func (r *credentialResponse) Close() error { r.closed = true; return nil }

type credentialStore struct {
	credential registryauth.Credential
	err        error
}

func (s credentialStore) Get(string) (registryauth.Credential, error) { return s.credential, s.err }
func (s credentialStore) Put(registryauth.Credential) error           { return nil }
func (s credentialStore) Delete(string) error                         { return nil }

func TestCredentialPreparationCachedRunDoesNotOpenKeychain(t *testing.T) {
	var output bytes.Buffer
	response := &credentialResponse{}
	w := &credentialOutput{reference: "ghcr.io/team/app:v1", response: response, output: &output, openStore: func() (registryauth.Store, error) { t.Fatal("cached run read Keychain"); return nil, nil }}
	id := "sha256:" + strings.Repeat("a", 64) + "\n"
	for _, fragment := range []string{id[:12], id[12:]} {
		if _, err := io.WriteString(w, fragment); err != nil {
			t.Fatal(err)
		}
	}
	if output.String() != id || response.Len() != 0 || !response.closed {
		t.Fatal("cached protocol changed")
	}
}

func TestCredentialPreparationScopeMissingAndStorageFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		store     credentialStore
		wantError bool
	}{
		{"saved", credentialStore{credential: registryauth.Credential{Registry: "ghcr.io", Username: "alice", Password: "test-token"}}, false},
		{"anonymous", credentialStore{err: registryauth.ErrNotFound}, false},
		{"locked", credentialStore{err: errors.New("Keychain locked")}, true},
		{"scope", credentialStore{credential: registryauth.Credential{Registry: "other.io", Username: "alice", Password: "test-token"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			response := &credentialResponse{}
			w := &credentialOutput{reference: "ghcr.io/team/app:v1", response: response, output: &output, openStore: func() (registryauth.Store, error) { return tc.store, nil }}
			for _, fragment := range []string{registryauth.RequestMarker[:7], registryauth.RequestMarker[7:]} {
				_, _ = io.WriteString(w, fragment)
			}
			if (w.err != nil) != tc.wantError || !response.closed || output.Len() != 0 {
				t.Fatalf("unexpected protocol result: %v", w.err)
			}
			if tc.wantError {
				if response.Len() != 0 {
					t.Fatal("failed lookup sent credentials")
				}
				return
			}
			credential, err := registryauth.Decode(bytes.NewReader(response.Bytes()), "ghcr.io")
			if err != nil || credential.Password != tc.store.credential.Password {
				t.Fatal("envelope changed")
			}
		})
	}
}

func TestPreparedImageArgumentsPreserveWorkloadAndOtherValues(t *testing.T) {
	args := []string{"run", "--env", "OPTION=--image", "--image=ghcr.io/app:v1", "--", "echo", "--image", "workload"}
	actual := preparedImageArguments(args, "sha256:id")
	if strings.Join(actual, "|") != "run|--env|OPTION=--image|--image=sha256:id|--|echo|--image|workload" {
		t.Fatalf("arguments changed: %q", actual)
	}
	for _, value := range []string{"--image", "--image=literal"} {
		args := []string{"run", "--image", "ghcr.io/app:v1", "--entrypoint", value, "--health-cmd", value, "--", "echo", value}
		actual := preparedImageArguments(args, "sha256:id")
		if actual[2] != "sha256:id" || actual[4] != value || actual[6] != value || actual[9] != value {
			t.Fatalf("literal option value changed: %q", actual)
		}
	}
}

func TestCredentialPreparationBoundsAndDisconnectedLargeEnvelope(t *testing.T) {
	response := &credentialResponse{}
	w := &credentialOutput{response: response, output: io.Discard}
	if _, err := io.WriteString(w, strings.Repeat("a", 129)+"\n"); err == nil {
		t.Fatal("oversized first line accepted")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	defer writer.Close()
	w = &credentialOutput{reference: "ghcr.io/team/app:v1", response: writer, output: io.Discard, openStore: func() (registryauth.Store, error) {
		return credentialStore{credential: registryauth.Credential{Registry: "ghcr.io", Username: "alice", Password: strings.Repeat("x", 16*1024)}}, nil
	}}
	done := make(chan error, 1)
	go func() { _, err := io.WriteString(w, registryauth.RequestMarker); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("disconnected transport succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("credential write hung after disconnect")
	}
}

func TestCredentialPreparationExactEnvelopeLimit(t *testing.T) {
	credential := registryauth.Credential{Registry: "ghcr.io", Username: "alice"}
	empty, _ := json.Marshal(credential)
	remaining := registryauth.MaxEnvelope - len(empty)
	credential.Password = strings.Repeat("\x01", remaining/6) + strings.Repeat("x", remaining%6)
	if err := credential.Validate("ghcr.io"); err != nil {
		t.Fatal(err)
	}
	response := &credentialResponse{}
	w := &credentialOutput{reference: "ghcr.io/team/app:v1", response: response, output: io.Discard, openStore: func() (registryauth.Store, error) { return credentialStore{credential: credential}, nil }}
	if _, err := io.WriteString(w, registryauth.RequestMarker); err != nil {
		t.Fatal(err)
	}
	if response.Len() != registryauth.MaxEnvelope {
		t.Fatalf("exact envelope changed length: %d", response.Len())
	}
	actual, err := registryauth.Decode(bytes.NewReader(response.Bytes()), "ghcr.io")
	if err != nil || actual != credential {
		t.Fatalf("guest rejected exact envelope: %v", err)
	}
}
