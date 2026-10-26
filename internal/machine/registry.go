package machine

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/registryauth"
	"github.com/mingo-liu/casklet/internal/runargs"
)

// credentialOutput handles only the first protocol line. Keychain is opened
// lazily after a cache miss; credentials travel once through a non-PTY pipe.
type credentialOutput struct {
	reference string
	response  io.WriteCloser
	output    io.Writer
	openStore func() (registryauth.Store, error)
	buffer    []byte
	started   bool
	completed bool
	err       error
}

func (w *credentialOutput) Write(data []byte) (int, error) {
	size := len(data)
	if w.err != nil {
		return 0, w.err
	}
	w.buffer = append(w.buffer, data...)
	if len(w.buffer) > 256 {
		w.err = errors.New("invalid registry preparation response")
		_ = w.response.Close()
		return 0, w.err
	}
	index := bytes.IndexByte(w.buffer, '\n')
	if index < 0 {
		return size, nil
	}
	line := string(w.buffer[:index+1])
	remaining := append([]byte(nil), w.buffer[index+1:]...)
	w.buffer = nil
	if !w.started && line == registryauth.RequestMarker {
		w.started = true
		w.err = w.sendCredential()
		if w.err != nil {
			_ = w.response.Close()
			return 0, w.err
		}
		if len(remaining) > 0 {
			_, w.err = w.Write(remaining)
		}
		return size, w.err
	}
	if w.completed || len(line) != 72 || !strings.HasPrefix(line, "sha256:") || len(remaining) != 0 {
		w.err = errors.New("invalid prepared image identity")
	} else if _, err := hex.DecodeString(line[7:71]); err != nil {
		w.err = errors.New("invalid prepared image identity")
	} else {
		w.completed = true
		_, w.err = io.WriteString(w.output, line)
	}
	_ = w.response.Close()
	return size, w.err
}

func (w *credentialOutput) sendCredential() error {
	defer w.response.Close()
	if pipe, ok := w.response.(interface{ SetWriteDeadline(time.Time) error }); ok {
		if err := pipe.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
			return errors.New("set credential transport deadline failed")
		}
	}
	registry, err := registryauth.Registry(w.reference)
	if err != nil {
		return err
	}
	open := w.openStore
	if open == nil {
		open = registryauth.OpenStore
	}
	store, err := open()
	if err != nil {
		return err
	}
	credential, err := store.Get(registry)
	if errors.Is(err, registryauth.ErrNotFound) {
		credential, err = registryauth.Credential{Registry: registry}, nil
	}
	if err != nil {
		return err
	}
	if err := credential.Validate(registry); err != nil {
		return err
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return errors.New("encode registry credentials failed")
	}
	defer clear(encoded)
	if n, err := w.response.Write(encoded); err != nil || n != len(encoded) {
		return errors.New("send registry credentials failed")
	}
	return nil
}

// Replace the last image option in the expanded CLI flags with the prepared
// immutable ID. A concurrent cache removal fails explicitly rather than causing
// a second anonymous download; workload arguments remain byte-for-byte intact.
func preparedImageArguments(args []string, id string) []string {
	result := append([]string(nil), args...)
	imageIndex, imageEnd, end := -1, -1, 1
	_ = runargs.Walk(result, func(name, _ string, index, last int) error {
		end = last + 1
		if name == "image" {
			imageIndex, imageEnd = index, last
		}
		return nil
	})
	if imageIndex >= 0 {
		runargs.SetValue(result, imageIndex, imageEnd, id)
		return result
	}
	return append(append(append([]string(nil), result[:end]...), "--image", id), result[end:]...)
}
