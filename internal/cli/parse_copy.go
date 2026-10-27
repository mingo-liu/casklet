package cli

import (
	"errors"
	"flag"
	"io"
	"path"
	"strings"
)

type copyEndpoint struct{ Container, Path string }

func parseCopyEndpoint(value string) (copyEndpoint, error) {
	endpoint := copyEndpoint{Path: value}
	colon, slash := strings.IndexByte(value, ':'), strings.IndexByte(value, '/')
	if colon >= 0 && (slash < 0 || colon < slash) {
		endpoint.Container, endpoint.Path = value[:colon], value[colon+1:]
		if endpoint.Container == "" || strings.HasPrefix(endpoint.Container, "-") {
			return endpoint, errors.New("copy requires a container name or ID before :")
		}
		for _, c := range endpoint.Container {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.-", c)) {
				return endpoint, errors.New("invalid copy container reference")
			}
		}
		if len(endpoint.Container) > 63 {
			return endpoint, errors.New("invalid copy container reference")
		}
		if err := validateCopyPath(endpoint.Path); err != nil {
			return endpoint, err
		}
	}
	if endpoint.Path == "" || endpoint.Path == "-" || strings.ContainsRune(endpoint.Path, 0) {
		return endpoint, errors.New("copy requires a filesystem path; stdin/stdout archives are unsupported")
	}
	return endpoint, nil
}

func validateCopyPath(value string) error {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("invalid container copy path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return errors.New("container copy path cannot contain ..")
		}
	}
	return nil
}

func parseCopy(r Request, args []string, private bool) (Request, error) {
	if private {
		if len(args) != 2 || args[0] == "" {
			return r, errors.New("invalid private copy request")
		}
		if err := validateCopyPath(args[1]); err != nil {
			return r, err
		}
		r.Reference, r.CopyPath = args[0], args[1]
		return r, nil
	}
	fs := flag.NewFlagSet("cp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp("cp", fs, args)
		}
		return r, err
	}
	if fs.NArg() != 2 {
		return r, errors.New("cp requires SOURCE and DESTINATION; exactly one must be CONTAINER:PATH")
	}
	var err error
	r.CopySource, err = parseCopyEndpoint(fs.Arg(0))
	if err != nil {
		return r, err
	}
	r.CopyTarget, err = parseCopyEndpoint(fs.Arg(1))
	if err != nil {
		return r, err
	}
	if (r.CopySource.Container == "") == (r.CopyTarget.Container == "") {
		return r, errors.New("cp requires exactly one container endpoint")
	}
	return r, nil
}

func containerCopyPath(value string) string {
	contents := strings.HasSuffix(value, "/.")
	directory := strings.HasSuffix(value, "/")
	value = strings.TrimPrefix(path.Clean("/"+value), "/")
	if value == "" {
		value = "."
	}
	if contents {
		value += "/."
	} else if directory {
		value += "/"
	}
	return value
}
