// The bounded guest helper serves native OCI fixtures for host progress tests.
package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/mingo-liu/casklet/internal/template"
)

func fixture() (v1.Image, error) {
	toolbox, err := os.ReadFile(template.BuiltinPath + "/bin/busybox")
	if err != nil {
		return nil, err
	}
	img := empty.Image
	nonce := fmt.Sprintf("progress-%d", time.Now().UnixNano())
	for _, file := range []struct {
		name string
		data []byte
	}{
		{"bin/busybox", toolbox},
		{"fixture", []byte(nonce)},
	} {
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		if err := writer.WriteHeader(&tar.Header{Name: file.name, Mode: 0755, Size: int64(len(file.data)), PAXRecords: map[string]string{"casklet.fixture": nonce}}); err != nil {
			return nil, err
		}
		if _, err := writer.Write(file.data); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		layer, err := tarball.LayerFromReader(bytes.NewReader(archive.Bytes()))
		if err != nil {
			return nil, err
		}
		img, err = mutate.AppendLayers(img, layer)
		if err != nil {
			return nil, err
		}
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	cf.OS, cf.Architecture = "linux", runtime.GOARCH
	cf.Config.Cmd = []string{"/bin/busybox", "echo", "image-progress-ok"}
	cf.Config.Healthcheck = &v1.HealthConfig{Test: []string{"CMD", "/bin/busybox", "test", "-f", "/fixture"}, Interval: 50 * time.Millisecond, Timeout: time.Second, Retries: 2}
	return mutate.ConfigFile(img, cf)
}

func main() {
	// An abandoned host test cannot leave an unbounded guest service.
	time.AfterFunc(2*time.Minute, func() { os.Exit(1) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	var mutex sync.Mutex
	downloads := map[string]int{}
	registryHandler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/casklet-test-downloads" {
			mutex.Lock()
			defer mutex.Unlock()
			_ = json.NewEncoder(w).Encode(downloads)
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/blobs/") {
			mutex.Lock()
			downloads[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]]++
			mutex.Unlock()
		}
		registryHandler.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	img, err := fixture()
	if err != nil {
		log.Fatal(err)
	}
	refs := make([]string, 2)
	for i, tag := range []string{"plain", "tty"} {
		ref, err := name.ParseReference(listener.Addr().String() + "/casklet/progress:" + tag)
		if err != nil {
			log.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			log.Fatal(err)
		}
		refs[i] = ref.Name()
	}
	manifest, err := img.Manifest()
	if err != nil {
		log.Fatal(err)
	}
	layers := make([]string, len(manifest.Layers))
	for i, layer := range manifest.Layers {
		layers[i] = layer.Digest.String()
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		References []string
		PID        int
		Layers     []string
		MetricsURL string
	}{refs, os.Getpid(), layers, "http://" + listener.Addr().String() + "/casklet-test-downloads"}); err != nil {
		log.Fatal(err)
	}
	select {}
}
