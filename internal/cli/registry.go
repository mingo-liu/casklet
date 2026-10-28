package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/mingo-liu/casklet/internal/image"
	"github.com/mingo-liu/casklet/internal/registryauth"
)

func parseRegistryCommand(r Request, args []string) (Request, error) {
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if r.Action == "login" {
		fs.StringVar(&r.Username, "username", "", "registry username")
		fs.StringVar(&r.Username, "u", "", "registry username")
		fs.BoolVar(&r.PasswordStdin, "password-stdin", false, "read password from stdin")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp(r.Action, fs, args)
		}
		return Request{}, err
	}
	if fs.NArg() > 1 {
		return Request{}, fmt.Errorf("%s accepts at most one registry", r.Action)
	}
	registry := ""
	if fs.NArg() == 1 {
		registry = fs.Arg(0)
	}
	var err error
	r.Reference, err = registryauth.NormalizeRegistry(registry)
	if err != nil {
		return Request{}, err
	}
	if r.Action == "login" && (!r.PasswordStdin || r.Username == "") {
		return Request{}, errors.New("login requires --username and --password-stdin")
	}
	return r, nil
}

func executeRegistryCommand(r Request, input io.Reader, output io.Writer, store registryauth.Store) error {
	if r.Action == "logout" {
		if err := store.Delete(r.Reference); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, "Removed credentials for "+r.Reference)
		return err
	}
	password, err := registryauth.ReadPassword(input)
	if err != nil {
		return err
	}
	c := registryauth.Credential{Registry: r.Reference, Username: r.Username, Password: password}
	if err := c.Validate(r.Reference); err != nil {
		return err
	}
	if err := store.Put(c); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "Saved credentials for "+r.Reference+" in macOS Keychain; access is checked on pull.")
	return err
}

// ExecuteCredentialPrepare is a private non-PTY protocol. A cache hit returns
// its immutable ID before requesting host credentials, preserving offline use.
// Credential bytes never become environment variables, flags, or saved config.
func ExecuteCredentialPrepare(args []string, input io.Reader, output, diagnostic io.Writer) int {
	if (len(args) != 3 && len(args) != 4) || (args[0] != "pull" && args[0] != "resolve" && args[0] != "push") || (args[0] == "push") != (len(args) == 4) || validateProgressMode(args[1]) != nil {
		fmt.Fprintln(diagnostic, "casklet: invalid credential preparation request")
		return 125
	}
	r := Request{Action: "image-pull", Reference: args[2], Progress: args[1]}
	return manageOperation(r, diagnostic, func(ctx context.Context) (int, error) {
		store, err := image.OpenStore()
		if err != nil {
			return 125, err
		}
		if args[0] == "resolve" {
			record, err := store.Resolve(ctx, r.Reference)
			if err == nil {
				_, err = fmt.Fprintln(output, record.ID)
				return 0, err
			}
			if !errors.Is(err, image.ErrNotFound) || image.IsIDReference(r.Reference) {
				return 125, err
			}
		}
		if args[0] == "push" {
			if _, err := store.Resolve(ctx, args[3]); err != nil {
				return 125, err
			}
		}
		ref, err := image.NormalizeReference(r.Reference)
		if err != nil {
			return 125, err
		}
		registry, err := registryauth.Registry(ref)
		if err != nil {
			return 125, err
		}
		if _, err := io.WriteString(output, registryauth.RequestMarker); err != nil {
			return 125, err
		}
		handshake, cancelHandshake := context.WithTimeout(ctx, 30*time.Second)
		credential, err := registryauth.DecodeContext(handshake, input, registry)
		cancelHandshake()
		if err != nil {
			return 125, err
		}
		ctx = registryauth.WithCredential(ctx, credential)
		if args[0] == "push" {
			digest, err := store.Push(ctx, args[3], ref)
			if err == nil {
				_, err = fmt.Fprintln(output, digest)
			}
			return 0, err
		}
		progress := newPullProgress(diagnostic, r.Progress)
		ctx = image.WithProgress(ctx, progress.observe)
		record, err := store.Pull(ctx, ref)
		if err == nil {
			_, err = fmt.Fprintln(output, record.ID)
		}
		return 0, err
	})
}
