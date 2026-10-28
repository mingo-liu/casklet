package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

const maxHistoryBytes = 16 << 20
const maxDeploymentBytes = 4 << 20
const maxDeployments = 32

// Deployment retains private effective inputs. Public history uses summaries;
// commands and expanded environment assignments must never be printed here.
type Deployment struct {
	Containers   map[string]string `json:"containers"`
	Revision     uint64            `json:"revision"`
	CreatedAt    time.Time         `json:"created_at"`
	RestoredFrom uint64            `json:"restored_from,omitempty"`
	Plan         Plan              `json:"plan"`
	Images       map[string]string `json:"images"`
}

type History struct {
	Version     int          `json:"version"`
	Name        string       `json:"name"`
	Deployments []Deployment `json:"deployments"`
}

type DeploymentSummary struct {
	Revision     uint64            `json:"revision"`
	CreatedAt    time.Time         `json:"created_at"`
	RestoredFrom uint64            `json:"restored_from,omitempty"`
	Services     []string          `json:"services"`
	Images       map[string]string `json:"images"`
}

func (h History) Summaries() []DeploymentSummary {
	summaries := make([]DeploymentSummary, 0, len(h.Deployments))
	for _, d := range h.Deployments {
		summary := DeploymentSummary{Revision: d.Revision, CreatedAt: d.CreatedAt, RestoredFrom: d.RestoredFrom, Images: d.Images, Services: []string{}}
		for _, service := range d.Plan.Services {
			summary.Services = append(summary.Services, service.Key)
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func ValidateDeploymentSize(plan Plan) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if len(data) > maxDeploymentBytes {
		return errors.New("deployment configuration exceeds 4 MiB history limit")
	}
	return nil
}

func (store *Store) LoadHistory() (History, error) {
	h := History{Version: 1, Name: store.name, Deployments: []Deployment{}}
	file, err := os.OpenFile(filepath.Join(store.root, store.name+".history.json"), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return h, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return h, err
	}
	if err := safeFile(info, false); err != nil {
		return h, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxHistoryBytes+1))
	if err != nil {
		return h, err
	}
	if len(data) > maxHistoryBytes {
		return h, errors.New("project history exceeds 16 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&h) != nil || decoder.Decode(new(any)) != io.EOF {
		return h, errors.New("invalid project history")
	}
	if err := store.validateHistory(h); err != nil {
		return h, err
	}
	return h, nil
}

func (store *Store) validateHistory(h History) error {
	if h.Version != 1 || h.Name != store.name || len(h.Deployments) > maxDeployments {
		return errors.New("invalid project history identity")
	}
	var last uint64
	for _, d := range h.Deployments {
		if d.Revision <= last || d.CreatedAt.IsZero() || d.Plan.Name != store.name || len(d.Plan.Services) == 0 || len(d.Plan.Services) > 64 || len(d.Containers) != len(d.Plan.Services) || d.RestoredFrom >= d.Revision {
			return errors.New("invalid deployment history identity")
		}
		last = d.Revision
		if err := ValidateDeploymentSize(d.Plan); err != nil {
			return err
		}
		services := map[string]bool{}
		for _, s := range d.Plan.Services {
			name, err := ScopedName(store.name, s.Key)
			if err != nil || name != s.Name || services[s.Key] || len(s.Args) == 0 || s.Args[0] != "run" {
				return errors.New("invalid deployment service identity")
			}
			services[s.Key] = true
			if !validDigest(d.Containers[s.Key], 32) {
				return errors.New("invalid deployment container identity")
			}
			imageID, found := d.Images[s.Key]
			if (s.Config.Image != "") != found || found && config.ValidateImageID(imageID) != nil {
				return errors.New("invalid pinned deployment image")
			}
		}
		for key := range d.Images {
			if !services[key] {
				return errors.New("unknown deployment image service")
			}
		}
	}
	return nil
}

// RecordDeployment atomically appends a successful deployment under the existing
// project lock. Oldest revisions expire at 32 entries or 16 MiB; IDs never reuse.
func (store *Store) RecordDeployment(h History, plan Plan, images, containers map[string]string, restoredFrom uint64) error {
	if err := ValidateDeploymentSize(plan); err != nil {
		return err
	}
	d := Deployment{Revision: 1, CreatedAt: time.Now().UTC(), RestoredFrom: restoredFrom, Plan: plan, Images: images, Containers: containers}
	if n := len(h.Deployments); n > 0 {
		previous := h.Deployments[n-1]
		if previous.Revision == ^uint64(0) {
			return errors.New("deployment revision exhausted")
		}
		d.Revision = previous.Revision + 1
		before, _ := json.Marshal(struct {
			Plan       Plan
			Images     map[string]string
			Containers map[string]string
		}{previous.Plan, previous.Images, previous.Containers})
		after, _ := json.Marshal(struct {
			Plan       Plan
			Images     map[string]string
			Containers map[string]string
		}{plan, images, containers})
		if restoredFrom == 0 && bytes.Equal(before, after) {
			return nil
		}
	}
	h.Deployments = append(h.Deployments, d)
	if len(h.Deployments) > maxDeployments {
		h.Deployments = h.Deployments[len(h.Deployments)-maxDeployments:]
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	for len(data) > maxHistoryBytes && len(h.Deployments) > 1 {
		h.Deployments = h.Deployments[1:]
		data, err = json.Marshal(h)
		if err != nil {
			return err
		}
	}
	if err := store.validateHistory(h); err != nil {
		return err
	}
	file, err := os.CreateTemp(store.root, ".history-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(store.root, store.name+".history.json")); err != nil {
		return err
	}
	dir, err := os.Open(store.root)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync project history: %w", err)
	}
	return nil
}
