// Package registryauth owns registry credentials, their host storage, and the
// bounded, operation-scoped host/guest authentication protocol.
package registryauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

const MaxEnvelope = 32 * 1024
const RequestMarker = "CASKLET_REGISTRY_CREDENTIAL_V1\n"

var ErrNotFound = errors.New("registry credentials not found")

type Credential struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Store never exposes credentials through command arguments or plaintext files.
type Store interface {
	Get(string) (Credential, error)
	Put(Credential) error
	Delete(string) error
}

// NormalizeRegistry accepts only a registry authority, never a URL/repository.
func NormalizeRegistry(value string) (string, error) {
	value = strings.ToLower(value)
	if value == "" {
		value = "index.docker.io"
	}
	if value == "docker.io" || value == "registry-1.docker.io" {
		value = "index.docker.io"
	}
	u, err := url.Parse("https://" + value)
	if err != nil || u.Host != value || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(value, " \t\r\n\x00\\") || len(value) > 255 {
		return "", errors.New("registry must be a hostname with optional port, without a scheme or path")
	}
	host := u.Hostname()
	if host == "" {
		return "", errors.New("registry hostname is required")
	}
	if net.ParseIP(host) == nil {
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", errors.New("invalid registry hostname")
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
					return "", errors.New("invalid registry hostname")
				}
			}
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid registry port")
		}
	}
	if strings.HasSuffix(value, ":") {
		return "", errors.New("invalid registry port")
	}
	return value, nil
}

func Registry(reference string) (string, error) {
	ref, err := name.ParseReference(reference)
	if err != nil {
		return "", errors.New("invalid image reference")
	}
	return NormalizeRegistry(ref.Context().RegistryStr())
}

func (c Credential) Validate(registry string) error {
	normalized, err := NormalizeRegistry(c.Registry)
	if err != nil || c.Registry != normalized || normalized != registry {
		return errors.New("registry credential scope mismatch")
	}
	if len(c.Username) > 1024 || strings.ContainsAny(c.Username, ":\x00\r\n") || len(c.Password) > 16*1024 || strings.ContainsAny(c.Password, "\x00\r\n") || (c.Username == "") != (c.Password == "") {
		return errors.New("invalid registry credentials")
	}
	encoded, err := json.Marshal(c)
	if err != nil || len(encoded) > MaxEnvelope || !utf8.ValidString(c.Username) || !utf8.ValidString(c.Password) {
		return errors.New("registry credential envelope exceeds bounds")
	}
	return nil
}

func ReadPassword(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 16*1024+3))
	if err != nil {
		return "", errors.New("read registry password from stdin failed")
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if len(password) == 0 || len(password) > 16*1024 || strings.ContainsAny(password, "\x00\r\n") {
		return "", errors.New("password must be one nonempty line of at most 16 KiB")
	}
	return password, nil
}

func Decode(r io.Reader, registry string) (Credential, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxEnvelope+1))
	if err != nil || len(data) > MaxEnvelope {
		return Credential{}, errors.New("invalid registry credential envelope")
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	var credential Credential
	if d.Decode(&credential) != nil || d.Decode(new(any)) != io.EOF {
		return Credential{}, errors.New("invalid registry credential envelope")
	}
	if err := credential.Validate(registry); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

// DecodeContext bounds a stalled host handshake and closes a pipe on caller
// cancellation so the guest CLI can honor forwarded signals during preparation.
func DecodeContext(ctx context.Context, r io.Reader, registry string) (Credential, error) {
	type result struct {
		credential Credential
		err        error
	}
	done := make(chan result, 1)
	go func() { c, err := Decode(r, registry); done <- result{c, err} }()
	select {
	case decoded := <-done:
		return decoded.credential, decoded.err
	case <-ctx.Done():
		if closer, ok := r.(io.Closer); ok {
			_ = closer.Close()
		}
		return Credential{}, ctx.Err()
	}
}

type credentialKey struct{}

func WithCredential(ctx context.Context, c Credential) context.Context {
	return context.WithValue(ctx, credentialKey{}, c)
}

func Authenticator(ctx context.Context, registry string) authn.Authenticator {
	c, ok := ctx.Value(credentialKey{}).(Credential)
	if !ok || c.Validate(registry) != nil || c.Username == "" {
		return authn.Anonymous
	}
	return authn.FromConfig(authn.AuthConfig{Username: c.Username, Password: c.Password})
}

// PublicError prevents registry response bodies or authentication service URLs
// from reflecting secrets into CLI output. Cancellation remains recognizable.
func PublicError(ctx context.Context, err error) error {
	return publicError(ctx, err, "pull")
}

func PublicPushError(ctx context.Context, err error) error {
	return publicError(ctx, err, "push")
}

func publicError(ctx context.Context, err error, operation string) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c, ok := ctx.Value(credentialKey{}).(Credential); ok && c.Username != "" {
		// Joined/wrapped cancellation errors may still contain a registry's
		// reflected response body. Return only the safe sentinel in this case.
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		return errors.New("authenticated registry " + operation + " failed; check credentials, repository access, and registry connectivity")
	}
	return err
}
