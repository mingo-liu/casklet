package registryauth

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// TokenRealm rejects plaintext credential exchanges outside a local development
// registry. HTTPS registries cannot downgrade authentication through a challenge.
func TokenRealm(value string, registry *url.URL) (*url.URL, error) {
	realm, err := url.Parse(value)
	if err != nil || realm.Host == "" || realm.User != nil || realm.Fragment != "" || !realm.IsAbs() {
		return nil, errors.New("invalid registry token realm")
	}
	if realm.Scheme == "https" {
		return realm, nil
	}
	if realm.Scheme == "http" && registry.Scheme == "http" && loopbackHost(registry.Hostname()) && loopbackHost(realm.Hostname()) {
		return realm, nil
	}
	return nil, errors.New("registry token realm must use HTTPS; HTTP is permitted only for loopback development")
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func ValidateRegistryEndpoint(endpoint *url.URL) error {
	if endpoint.Scheme == "https" || endpoint.Scheme == "http" && loopbackHost(endpoint.Hostname()) {
		return nil
	}
	return errors.New("authenticated registry endpoints require HTTPS except loopback development")
}

// ScopedTransport strips inherited authorization even on redirects between
// ports of the same hostname, which net/http otherwise considers trusted. The
// registry's declared token endpoint is the only additional credential target;
// token POST redirects outside that endpoint are rejected before sending a body.
type ScopedTransport struct {
	Base     http.RoundTripper
	Registry *url.URL
	Realm    *url.URL
}

func (t ScopedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	registry := request.URL.Scheme == t.Registry.Scheme && request.URL.Host == t.Registry.Host
	realm := t.Realm != nil && request.URL.Scheme == t.Realm.Scheme && request.URL.Host == t.Realm.Host && request.URL.Path == t.Realm.Path
	if !registry && !realm {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			return nil, errors.New("registry authentication redirect outside credential scope")
		}
		request = request.Clone(request.Context())
		request.Header.Del("Authorization")
		request.Header.Del("Proxy-Authorization")
	}
	return t.Base.RoundTrip(request)
}
