package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/image"
)

func TestImageListingTable(t *testing.T) {
	created := time.Date(2026, 10, 9, 10, 11, 12, 0, time.FixedZone("local", 3600))
	id := "sha256:406e742c72ac" + strings.Repeat("a", 52)
	digest := "sha256:" + strings.Repeat("b", 64)
	records := []image.Record{
		{ID: id, Architecture: "arm64", SizeBytes: 202000000, CreatedAt: created, References: []string{"index.docker.io/library/redis:latest", "index.docker.io/library/redis:8"}},
		{ID: "sha256:" + strings.Repeat("b", 64), Architecture: "amd64", SizeBytes: 1850000000, CreatedAt: created, References: []string{"registry.example:5000/team/app:v1"}},
		{ID: "sha256:" + strings.Repeat("c", 64), Architecture: "arm64", SizeBytes: 786000, CreatedAt: created},
		{ID: "sha256:" + strings.Repeat("d", 64), Architecture: "arm64", SizeBytes: 0, CreatedAt: created, References: []string{"index.docker.io/mingo/app@" + digest}},
	}
	var out bytes.Buffer
	if err := writeImages(&out, records, false); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"IMAGE", "ID", "ARCHITECTURE", "SIZE", "CREATED"},
		{"redis:latest", "406e742c72ac", "arm64", "202MB", "2026-10-09T09:11:12Z"},
		{"redis:8", "406e742c72ac", "arm64", "202MB", "2026-10-09T09:11:12Z"},
		{"registry.example:5000/team/app:v1", strings.Repeat("b", 12), "amd64", "1.85GB", "2026-10-09T09:11:12Z"},
		{"<none>", strings.Repeat("c", 12), "arm64", "786kB", "2026-10-09T09:11:12Z"},
		{"mingo/app@" + digest, strings.Repeat("d", 12), "arm64", "0B", "2026-10-09T09:11:12Z"},
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("unexpected image rows: %q", out.String())
	}
	for i, line := range lines {
		if got := strings.Fields(line); !reflect.DeepEqual(got, want[i]) {
			t.Errorf("row %d = %q, want %q", i, got, want[i])
		}
	}
	// Rendering must not rewrite identities or canonical references in metadata.
	out.Reset()
	if err := writeImages(&out, records, true); err != nil {
		t.Fatal(err)
	}
	var decoded []image.Record
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for i, record := range decoded {
		if record.ID != records[i].ID || record.SizeBytes != records[i].SizeBytes || !reflect.DeepEqual(record.References, records[i].References) {
			t.Fatalf("JSON metadata changed: %+v", record)
		}
	}
}

func TestImageSizeUnitsAndRounding(t *testing.T) {
	for _, test := range []struct {
		bytes int64
		want  string
	}{
		{0, "0B"}, {1, "1B"}, {999, "999B"}, {1000, "1kB"},
		{1234, "1.23kB"}, {12345, "12.3kB"}, {786000, "786kB"},
		{999499, "999kB"}, {999500, "1MB"}, {1000000, "1MB"},
		{202000000, "202MB"}, {1850000000, "1.85GB"},
		{1000000000000, "1TB"}, {1000000000000000, "1PB"},
		{1000000000000000000, "1EB"}, {1<<63 - 1, "9.22EB"},
	} {
		if got := displayImageSize(test.bytes); got != test.want {
			t.Errorf("size %d = %q, want %q", test.bytes, got, test.want)
		}
	}
}

func TestImageListingEmptyAndWriteFailure(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		var out bytes.Buffer
		if err := writeImages(&out, nil, asJSON); err != nil {
			t.Fatal(err)
		}
		if asJSON && out.String() != "[]\n" {
			t.Fatalf("empty JSON listing = %q", out.String())
		}
		if !asJSON && !reflect.DeepEqual(strings.Fields(out.String()), []string{"IMAGE", "ID", "ARCHITECTURE", "SIZE", "CREATED"}) {
			t.Fatalf("empty table = %q", out.String())
		}
		if err := writeImages(failingWriter{}, nil, asJSON); err == nil {
			t.Error("listing ignored output failure")
		}
	}
}
