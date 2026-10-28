//go:build linux

package image

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/mingo-liu/casklet/internal/registryauth"
)

// Tag atomically replaces a local mutable reference while pinning its image.
func (store *Store) Tag(ctx context.Context, source, target string) error {
	target, err := NormalizeTag(target)
	if err != nil {
		return err
	}
	record, err := store.Resolve(ctx, source)
	if err != nil {
		return err
	}
	_, _, lease, err := store.Acquire(ctx, record.ID)
	if err != nil {
		return err
	}
	defer lease.Close()
	refLock, err := store.lockReference(ctx, target)
	if err != nil {
		return err
	}
	defer refLock.Close()
	lock, err := store.lock(ctx, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	return store.publishReferenceLocked(ctx, target, record.ID)
}

// Push exports a verified snapshot, then publishes its manifest last. The local
// mapping is unchanged; registries may retain uploaded blobs after cancellation.
func (store *Store) Push(ctx context.Context, source, target string) (digest string, resultErr error) {
	publicContext := ctx
	defer func() { resultErr = registryauth.PublicPushError(publicContext, resultErr) }()
	target, err := NormalizeTag(target)
	if err != nil {
		return "", err
	}
	parsed, err := name.NewTag(target)
	if err != nil {
		return "", err
	}
	record, err := store.Resolve(ctx, source)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	stage, lease, transaction, err := store.beginImport(ctx)
	if err != nil {
		return "", err
	}
	defer lease.Close()
	defer store.cleanupImport(stage, transaction)
	archive, err := os.CreateTemp(stage, ".push-")
	if err != nil {
		return "", err
	}
	defer archive.Close()
	if err := store.save(ctx, record.ID, archive, true); err != nil {
		return "", err
	}
	if _, err := archive.Seek(0, 0); err != nil {
		return "", err
	}
	dir := filepath.Join(stage, ".oci")
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	a, err := readOCIArchive(ctx, dir, archive)
	if err != nil {
		return "", err
	}
	lp, err := layout.FromPath(a.dir)
	if err != nil {
		return "", err
	}
	index, err := lp.ImageIndex()
	if err != nil {
		return "", err
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		return "", err
	}
	if len(manifest.Manifests) != 1 {
		return "", errors.New("push requires one native image")
	}
	img, err := index.Image(manifest.Manifests[0].Digest)
	if err != nil {
		return "", err
	}
	options, err := registryOptions(ctx, parsed)
	if err != nil {
		return "", err
	}
	if err := remote.Write(parsed, img, options...); err != nil {
		return "", err
	}
	return manifest.Manifests[0].Digest.String(), nil
}

func registryOptions(ctx context.Context, parsed name.Reference) ([]remote.Option, error) {
	scope, err := registryauth.Registry(parsed.Name())
	if err != nil {
		return nil, err
	}
	auth := registryauth.Authenticator(ctx, scope)
	options := []remote.Option{remote.WithContext(ctx), remote.WithAuth(auth)}
	credential, _ := auth.Authorization()
	if credential.Username != "" {
		registry := parsed.Context().Registry
		endpoint, _ := url.Parse(registry.Scheme() + "://" + registry.RegistryStr())
		if err := registryauth.ValidateRegistryEndpoint(endpoint); err != nil {
			return nil, err
		}
		challenge, err := transport.Ping(ctx, registry, remote.DefaultTransport)
		if err != nil {
			return nil, err
		}
		var realm *url.URL
		if value := challenge.Parameters["realm"]; strings.EqualFold(challenge.Scheme, "bearer") && value != "" {
			realm, err = registryauth.TokenRealm(value, endpoint)
			if err != nil {
				return nil, err
			}
		}
		options = append(options, remote.WithTransport(registryauth.ScopedTransport{Base: remote.DefaultTransport, Registry: endpoint, Realm: realm}))
	}
	return options, nil
}
