//go:build linux

package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/mingo-liu/casklet/internal/registryauth"
)

type authTransportFunc func(*http.Request) (*http.Response, error)

var _ http.RoundTripper = authTransportFunc(nil)

func TestRegistryAuthenticatedPullBasicBearerScopeAndOfflineCache(t *testing.T) {
	for _, scheme := range []string{"Basic", "Bearer"} {
		t.Run(scheme, func(t *testing.T) {
			var (
				enabled atomic.Bool
				tokens  atomic.Int32
				handler = registry.New(registry.Logger(log.New(io.Discard, "", 0)))
				server  *httptest.Server
			)
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if enabled.Load() {
					user, password, valid := r.BasicAuth()
					if scheme == "Bearer" && r.URL.Path == "/token" {
						if !valid || user != "alice" || password != "fixture-token" {
							http.Error(w, "fixture-token reflected by auth service", http.StatusUnauthorized)
							return
						}
						tokens.Add(1)
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"token":"fixture-bearer","expires_in":300}`)
						return
					}
					authorized := valid && user == "alice" && password == "fixture-token"
					if scheme == "Bearer" {
						authorized = r.Header.Get("Authorization") == "Bearer fixture-bearer"
					}
					if !authorized {
						challenge := `Basic realm="fixture"`
						if scheme == "Bearer" {
							challenge = fmt.Sprintf(`Bearer realm="%s/token",service="fixture"`, server.URL)
						}
						w.Header().Set("WWW-Authenticate", challenge)
						http.Error(w, "fixture-token reflected by registry", http.StatusUnauthorized)
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			reference, _ := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/private/app:v1")
			img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "authenticated image"}))
			if err := remote.Write(reference, img); err != nil {
				t.Fatal(err)
			}
			enabled.Store(true)
			store, err := newStoreAt(filepath.Join(t.TempDir(), "images"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Pull(context.Background(), reference.Name()); err == nil || errors.Is(err, context.Canceled) {
				t.Fatalf("anonymous failure lost: %v", err)
			}
			var (
				credential = registryauth.Credential{Registry: reference.Context().RegistryStr(), Username: "alice", Password: "wrong-token"}
				ctx        = registryauth.WithCredential(context.Background(), credential)
			)
			if _, err := store.Pull(ctx, reference.Name()); err == nil || errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "token") || !strings.Contains(err.Error(), "authenticated registry pull failed") {
				t.Fatalf("authentication failure not sanitized: %v", err)
			}
			credential.Registry = "other.invalid"
			credential.Password = "fixture-token"
			if _, err := store.Pull(registryauth.WithCredential(context.Background(), credential), reference.Name()); err == nil {
				t.Fatal("credential used for wrong registry")
			}
			credential.Registry = reference.Context().RegistryStr()
			ctx = registryauth.WithCredential(context.Background(), credential)
			record, err := store.Pull(ctx, reference.Name())
			if err != nil {
				t.Fatal(err)
			}
			if scheme == "Bearer" && tokens.Load() < 1 {
				t.Fatal("token challenge not used")
			}
			server.Close()
			cached, err := store.Resolve(context.Background(), reference.Name())
			if err != nil || cached.ID != record.ID {
				t.Fatalf("offline cache failed: %v", err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := store.Pull(canceled, reference.Name()); !errors.Is(err, context.Canceled) {
				t.Fatalf("caller cancellation lost: %v", err)
			}
		})
	}
}

func TestAuthenticatedBlobRedirectDoesNotForwardCredentialsAcrossPorts(t *testing.T) {
	var (
		enabled    atomic.Bool
		redirected atomic.Int32
		leaked     atomic.Bool
		handler    = registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		handler.ServeHTTP(w, r)
	}))
	defer cdn.Close()
	var layerDigest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enabled.Load() {
			user, password, ok := r.BasicAuth()
			if !ok || user != "alice" || password != "fixture-token" {
				w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+layerDigest) {
				http.Redirect(w, r, cdn.URL+r.URL.Path, http.StatusTemporaryRedirect)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	reference, _ := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/private/redirect:v1")
	var (
		img           = fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "redirected image"}))
		manifest, err = img.Manifest()
	)
	if err != nil {
		t.Fatal(err)
	}
	layerDigest = manifest.Layers[0].Digest.String()
	if err := remote.Write(reference, img); err != nil {
		t.Fatal(err)
	}
	enabled.Store(true)
	store, err := newStoreAt(filepath.Join(t.TempDir(), "images"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := registryauth.WithCredential(context.Background(), registryauth.Credential{Registry: reference.Context().RegistryStr(), Username: "alice", Password: "fixture-token"})
	if _, err := store.Pull(ctx, reference.Name()); err != nil {
		t.Fatal(err)
	}
	if redirected.Load() < 1 || leaked.Load() {
		t.Fatalf("cross-port redirect: requests=%d leaked=%v", redirected.Load(), leaked.Load())
	}
}

func TestAuthenticatedPullRejectsPlaintextNonLoopbackTokenRealmBeforeSendingCredentials(t *testing.T) {
	const realm = "http://203.0.113.10/token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="fixture"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	var (
		tokenRequests atomic.Int32
		base          = remote.DefaultTransport
	)
	transport := authTransportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "203.0.113.10" {
			tokenRequests.Add(1)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"token":"fixture-bearer"}`)), Request: r}, nil
		}
		return base.RoundTrip(r)
	})
	reference, _ := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/private/app:v1")
	var (
		ctx        = registryauth.WithCredential(context.Background(), registryauth.Credential{Registry: reference.Context().RegistryStr(), Username: "alice", Password: "fixture-token"})
		store, err = newStoreAt(filepath.Join(t.TempDir(), "images"))
	)
	if err != nil {
		t.Fatal(err)
	}
	store.transport = transport
	if _, err := store.Pull(ctx, reference.Name()); err == nil || !strings.Contains(err.Error(), "authenticated registry pull failed") {
		t.Fatalf("unsafe realm accepted: %v", err)
	}
	if tokenRequests.Load() != 0 {
		t.Fatal("credentials sent to plaintext non-loopback token realm")
	}
}

func TestAuthenticatedPullRejectsPlaintextPrivateRegistryBeforeAnyRequest(t *testing.T) {
	var requests atomic.Int32
	transport := authTransportFunc(func(_ *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected network request")
	})
	var (
		ctx        = registryauth.WithCredential(context.Background(), registryauth.Credential{Registry: "10.1.2.3", Username: "alice", Password: "fixture-token"})
		store, err = newStoreAt(filepath.Join(t.TempDir(), "images"))
	)
	if err != nil {
		t.Fatal(err)
	}
	store.transport = transport
	if _, err := store.Pull(ctx, "10.1.2.3/private/app:v1"); err == nil || !strings.Contains(err.Error(), "authenticated registry pull failed") {
		t.Fatalf("unsafe registry accepted: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("plaintext registry received a request")
	}
}

func (f authTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
