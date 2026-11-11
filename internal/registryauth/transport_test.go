package registryauth

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func TestScopedTransportStripsRedirectAuthAndRejectsSecretPost(t *testing.T) {
	registry, _ := url.Parse("https://registry.example:443")
	realm, _ := url.Parse("https://auth.example/token")
	tests := []struct {
		url, method          string
		authorized, rejected bool
	}{
		{url: "https://registry.example:443/v2/app", method: "GET", authorized: true, rejected: false},
		{url: "https://registry.example:444/v2/app", method: "GET", authorized: false, rejected: false},
		{url: "http://registry.example:443/v2/app", method: "GET", authorized: false, rejected: false},
		{url: "https://auth.example/token?scope=pull", method: "POST", authorized: true, rejected: false},
		{url: "https://auth.example/other", method: "POST", authorized: false, rejected: true},
		{url: "https://cdn.example/blob", method: "GET", authorized: false, rejected: false},
		{url: "https://other.example/token", method: "POST", authorized: false, rejected: true},
	}
	for _, tt := range tests {
		called := false
		base := transportFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			if (r.Header.Get("Authorization") != "") != tt.authorized {
				t.Fatalf("authorization scope mismatch: %s", tt.url)
			}
			return &http.Response{StatusCode: 200}, nil
		})
		request, _ := http.NewRequest(tt.method, tt.url, strings.NewReader("sensitive body"))
		request.Header.Set("Authorization", "Basic fixture-secret")
		_, err := (ScopedTransport{Base: base, Registry: registry, Realm: realm}).RoundTrip(request)
		if (err != nil) != tt.rejected || called == tt.rejected {
			t.Fatalf("redirect handling: %s %v", tt.url, err)
		}
		if request.Header.Get("Authorization") == "" {
			t.Fatal("mutated original request")
		}
	}
}

func TestTokenRealmRequiresAbsoluteHTTPSOrLoopbackHTTP(t *testing.T) {
	tests := []struct {
		registry, realm string
		allowed         bool
	}{
		{"https://registry.example", "https://auth.example/token", true},
		{"https://registry.example", "http://auth.example/token", false},
		{"https://registry.example", "http://127.0.0.1/token", false},
		{"http://127.0.0.1:5000", "http://127.0.0.1:5000/token", true},
		{"http://localhost:5000", "http://[::1]:5001/token", true},
		{"http://127.0.0.1:5000", "http://auth.example/token", false},
		{"https://registry.example", "/token", false},
		{"https://registry.example", "https:///token", false},
		{"https://registry.example", "https://user:password@auth.example/token", false},
	}
	for _, tt := range tests {
		registry, _ := url.Parse(tt.registry)
		_, err := TokenRealm(tt.realm, registry)
		if (err == nil) != tt.allowed {
			t.Fatalf("realm policy: %q -> %q: %v", tt.registry, tt.realm, err)
		}
	}
}

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
