package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strconv"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
	"github.com/mingo-liu/casklet/internal/template"
)

func parseProjectReplace(args []string) (Request, error) {
	if len(args) != 5 || len(args[0]) != 32 || args[4] == "" {
		return Request{}, errors.New("invalid project replacement identity")
	}
	if _, err := hex.DecodeString(args[0]); err != nil {
		return Request{}, errors.New("invalid project replacement identity")
	}
	_, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil {
		return Request{}, errors.New("invalid project replacement generation")
	}
	if err := project.ValidateName(args[2]); err != nil {
		return Request{}, err
	}
	if err := project.ValidateName(args[3]); err != nil {
		return Request{}, err
	}
	return Request{Action: "project-replace", References: append([]string(nil), args...)}, nil
}

func executeProjectReplace(ctx context.Context, r Request) (int, error) {
	args := r.References
	generation, _ := strconv.ParseUint(args[1], 10, 64)
	return 0, container.RemoveProjectService(ctx, args[0], generation, map[string]string{project.LabelName: args[2], project.LabelService: args[3], project.LabelSpec: args[4]})
}

func executeProjectPreflight(ctx context.Context, r Request, out io.Writer) (int, error) {
	cfg := r.Config
	if cfg.Image != "" {
		resolved, lease, err := template.ResolveExecution(ctx, cfg, r.Entrypoint)
		if err != nil {
			return 125, err
		}
		defer lease.Close()
		cfg = resolved
	}
	if err := cfg.ValidateExecution(); err != nil {
		return 125, err
	}
	// Volume presence/ownership is checked in the project inventory. Managed
	// volumes may not exist yet, so only bind sources are opened here.
	mounts := cfg.Mounts[:0:0]
	for _, mount := range cfg.Mounts {
		if mount.Type != "volume" {
			mounts = append(mounts, mount)
		}
	}
	cfg.Mounts = mounts
	source, err := template.Acquire(ctx, cfg)
	if err != nil {
		return 125, err
	}
	defer source.Close()
	if cfg.Image != "" {
		_, err = io.WriteString(out, cfg.Image+"\n")
	}
	return 0, err
}

func executeProjectStartPreflight(ctx context.Context, r Request) (int, error) {
	args := r.References
	generation, _ := strconv.ParseUint(args[1], 10, 64)
	return 0, container.PreflightProjectStart(ctx, args[0], generation, map[string]string{project.LabelName: args[2], project.LabelService: args[3], project.LabelSpec: args[4]})
}

func parseProjectStartPreflight(args []string) (Request, error) {
	if len(args) < 5 {
		return Request{}, errors.New("invalid retained-source preflight")
	}
	r, err := parseProjectReplace(args[:5])
	if err != nil {
		return r, err
	}
	r.Action = "project-start-preflight"
	for _, value := range args[5:] {
		port, err := config.ParsePortMapping(value)
		if err != nil {
			return r, err
		}
		r.Config.Publish = append(r.Config.Publish, port)
	}
	return r, config.ValidateNetwork("bridge", nil, r.Config.Publish, nil)
}
