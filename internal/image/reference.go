package image

import (
	"errors"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// NormalizeTag accepts mutable names only; a digest cannot name arbitrary content.
func NormalizeTag(value string) (string, error) {
	ref, err := NormalizeReference(value)
	if err != nil {
		return "", err
	}
	if strings.Contains(ref, "@") {
		return "", errors.New("target must be NAME[:TAG], not a digest")
	}
	return ref, nil
}

// NormalizeReference supplies Docker Hub, library, and latest defaults.
func NormalizeReference(value string) (string, error) {
	if value == "" || strings.ContainsAny(value, " \t\r\n\x00") || strings.Contains(value, "://") || strings.HasPrefix(value, "sha256:") {
		return "", errors.New("image reference must be NAME[:TAG] or NAME@sha256:DIGEST")
	}
	ref, err := name.ParseReference(value, name.WeakValidation)
	if err != nil {
		return "", err
	}
	if registry := ref.Context().RegistryStr(); registry == "." || registry == ".." {
		return "", errors.New("invalid registry name")
	}
	for _, component := range strings.Split(ref.Context().RepositoryStr(), "/") {
		if component == "" || component == "." || component == ".." {
			return "", errors.New("invalid image repository component")
		}
	}
	return ref.Name(), nil
}
