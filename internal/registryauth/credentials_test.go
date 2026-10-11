package registryauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRegistryAndPasswordValidation(t *testing.T) {
	for input, expected := range map[string]string{"": "index.docker.io", "Docker.io": "index.docker.io", "registry-1.docker.io": "index.docker.io", "GHCR.IO": "ghcr.io", "localhost:5000": "localhost:5000", "[::1]:5000": "[::1]:5000"} {
		actual, err := NormalizeRegistry(input)
		if err != nil || actual != expected {
			t.Fatalf("%q: %q %v", input, actual, err)
		}
	}
	for _, input := range []string{"https://ghcr.io", "ghcr.io/team", "alice@ghcr.io", "ghcr.io?x=1", ".", "ghcr.io:", "ghcr.io:0", "ghcr.io:65536", "ghcr.io:abc", "bad\nregistry", "-invalid.io"} {
		if _, err := NormalizeRegistry(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	for _, input := range []string{"", "one\ntwo\n", "secret\x00", strings.Repeat("x", 16*1024+1)} {
		if _, err := ReadPassword(strings.NewReader(input)); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
	actual, err := ReadPassword(strings.NewReader(" spaces stay \r\n"))
	if err != nil || actual != " spaces stay " {
		t.Fatalf("password normalization: %q %v", actual, err)
	}
}

func TestCredentialEnvelopeScopeBoundsAndSanitizedErrors(t *testing.T) {
	c := Credential{Registry: "ghcr.io", Username: "alice", Password: "test-secret"}
	encoded, _ := json.Marshal(c)
	actual, err := Decode(strings.NewReader(string(encoded)), "ghcr.io")
	if err != nil || actual != c {
		t.Fatalf("decode: %+v %v", actual, err)
	}
	for _, input := range []string{string(encoded) + "{}", strings.Replace(string(encoded), "ghcr.io", "other.io", 1), `{"registry":"ghcr.io","unexpected":"test-secret"}`, strings.Repeat("x", MaxEnvelope+1)} {
		if _, err := Decode(strings.NewReader(input), "ghcr.io"); err == nil || strings.Contains(err.Error(), c.Password) {
			t.Fatal("invalid or unsanitized envelope")
		}
	}
	ctx := WithCredential(context.Background(), c)
	a, err := Authenticator(ctx, "ghcr.io").Authorization()
	if err != nil || a.Username != c.Username || a.Password != c.Password {
		t.Fatal("credential lost")
	}
	a, _ = Authenticator(ctx, "other.io").Authorization()
	if a.Username != "" || a.Password != "" {
		t.Fatal("credential leaked to another registry")
	}
	if strings.Contains(PublicError(ctx, errors.New("server reflected test-secret")).Error(), c.Password) {
		t.Fatal("error leaks password")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if !errors.Is(PublicError(canceled, errors.New("failed")), context.Canceled) {
		t.Fatal("cancellation lost")
	}
	if !errors.Is(PublicError(ctx, context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("pull deadline lost")
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		err := PublicError(ctx, errors.Join(errors.New("reflected test-secret"), sentinel))
		if !errors.Is(err, sentinel) || strings.Contains(err.Error(), "test-secret") {
			t.Fatal("joined cancellation leaked credentials")
		}
	}
	c.Password = strings.Repeat("\x01", 16*1024)
	if c.Validate("ghcr.io") == nil {
		t.Fatal("oversized JSON encoded envelope accepted")
	}
}

func TestCredentialHandshakeCancellationAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		reader, writer := io.Pipe()
		defer writer.Close()
		ctx, cancel := context.WithCancel(context.Background())
		if deadline {
			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
		}
		done := make(chan error, 1)
		go func() { _, err := DecodeContext(ctx, reader, "ghcr.io"); done <- err }()
		if !deadline {
			cancel()
		}
		select {
		case err := <-done:
			want := context.Canceled
			if deadline {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("handshake cancellation: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("stalled credential handshake did not cancel")
		}
		cancel()
	}
}
