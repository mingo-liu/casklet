package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestDeploymentHistoryRetentionPrivacyAndRecovery(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	store, err := Open(context.Background(), root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	history, err := store.LoadHistory()
	if err != nil || len(history.Deployments) != 0 {
		t.Fatal(err)
	}
	plan := Plan{Name: "demo", Services: []Service{{Key: "app", Name: "cp-demo-app", Args: []string{"run", "--env", "TOKEN=private-token"}, Config: config.Config{Image: "app:v1"}}}}
	images := map[string]string{"app": "sha256:" + strings.Repeat("a", 64)}
	for i := 0; i < 35; i++ {
		plan.Services[0].Fingerprint = strings.Repeat("x", i+1)
		if err := store.RecordDeployment(history, plan, images, map[string]string{"app": strings.Repeat("b", 32)}, 0); err != nil {
			t.Fatal(err)
		}
		history, err = store.LoadHistory()
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(history.Deployments) != 32 || history.Deployments[0].Revision != 4 || history.Deployments[31].Revision != 35 {
		t.Fatal("retention or sequence changed")
	}
	if err := store.RecordDeployment(history, plan, images, map[string]string{"app": strings.Repeat("b", 32)}, 0); err != nil {
		t.Fatal(err)
	}
	same, err := store.LoadHistory()
	if err != nil || len(same.Deployments) != 32 || same.Deployments[31].Revision != 35 {
		t.Fatal("no-op appended history")
	}
	if err := store.RecordDeployment(history, plan, images, map[string]string{"app": strings.Repeat("c", 32)}, 0); err != nil {
		t.Fatal(err)
	}
	history, err = store.LoadHistory()
	if err != nil || history.Deployments[31].Revision != 36 {
		t.Fatal("recreation did not record a deployment")
	}
	if err := store.RecordDeployment(history, plan, images, map[string]string{"app": strings.Repeat("b", 32)}, 4); err != nil {
		t.Fatal(err)
	}
	history, err = store.LoadHistory()
	if err != nil || history.Deployments[31].Revision != 37 || history.Deployments[31].RestoredFrom != 4 {
		t.Fatal("rollback provenance lost")
	}
	public, _ := json.Marshal(history.Summaries())
	if strings.Contains(string(public), "private-token") || strings.Contains(string(public), "TOKEN") {
		t.Fatal("history leaked execution data")
	}
	file := filepath.Join(root, "demo.history.json")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("history not private")
	}
	os.Chmod(file, 0644)
	if _, err := store.LoadHistory(); err == nil {
		t.Fatal("unsafe history accepted")
	}
	os.Chmod(file, 0600)
	os.WriteFile(file, []byte(`{"version":1,"name":"other","deployments":[]}`), 0600)
	if _, err := store.LoadHistory(); err == nil {
		t.Fatal("wrong identity accepted")
	}
}
