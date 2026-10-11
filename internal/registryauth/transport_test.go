package registryauth

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestScopedTransportStripsRedirectAuthAndRejectsSecretPost(t *testing.T) {
	registry, _ := url.Parse("https://registry.example:443")
	realm, _ := url.Parse("https://auth.example/token")
	for _, tc := range []struct {
		url, method          string
		authorized, rejected bool
	}{
		{"https://registry.example:443/v2/app", "GET", true, false},
		{"https://registry.example:444/v2/app", "GET", false, false},
		{"http://registry.example:443/v2/app", "GET", false, false},
		{"https://auth.example/token?scope=pull", "POST", true, false},
		{"https://auth.example/other", "POST", false, true},
		{"https://cdn.example/blob", "GET", false, false},
		{"https://other.example/token", "POST", false, true},
	} {
		called := false
		base := transportFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			if (r.Header.Get("Authorization") != "") != tc.authorized {
				t.Fatalf("authorization scope mismatch: %s", tc.url)
			}
			return &http.Response{StatusCode: 200}, nil
		})
		request, _ := http.NewRequest(tc.method, tc.url, strings.NewReader("sensitive body"))
		request.Header.Set("Authorization", "Basic fixture-secret")
		_, err := (ScopedTransport{Base: base, Registry: registry, Realm: realm}).RoundTrip(request)
		if (err != nil) != tc.rejected || called == tc.rejected {
			t.Fatalf("redirect handling: %s %v", tc.url, err)
		}
		if request.Header.Get("Authorization") == "" {
			t.Fatal("mutated original request")
		}
	}
}

func TestTokenRealmRequiresAbsoluteHTTPSOrLoopbackHTTP(t *testing.T) {
	for _, tc := range []struct {
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
	} {
		registry, _ := url.Parse(tc.registry)
		_, err := TokenRealm(tc.realm, registry)
		if (err == nil) != tc.allowed {
			t.Fatalf("realm policy: %q -> %q: %v", tc.registry, tc.realm, err)
		}
	}
}
