// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"errors"

	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/db"
)

var (
	ErrNotInstalled = errors.New("the service is not installed")
)

type LogOptions struct {
	Follow bool
	Lines  int
}

// SystemdServiceOption configures a SystemdService at construction time.
type SystemdServiceOption func(*SystemdService)

// WithTailscaleGuardRunner configures the exact stable Catch runner accepted
// by resolver-guarded Tailscale sidecar units.
func WithTailscaleGuardRunner(path string) SystemdServiceOption {
	return func(service *SystemdService) {
		service.tailscaleGuardRunner = path
	}
}

// WithSystemdDirectory configures the stable systemd unit directory used by
// install planning and lifecycle operations.
func WithSystemdDirectory(path string) SystemdServiceOption {
	return func(service *SystemdService) {
		service.systemdDir = path
	}
}

// NewSystemdService creates a new systemd service from a SystemdConfigView.
func NewSystemdService(db *db.Store, cfg db.ServiceView, runDir string, options ...SystemdServiceOption) (*SystemdService, error) {
	service := &SystemdService{db: db, cfg: cfg, runDir: runDir}
	for _, option := range options {
		option(service)
	}
	return service, nil
}

// NewHostSystemdService creates a privileged, self-managed host daemon that
// intentionally keeps the historical flat runtime artifact layout.
func NewHostSystemdService(db *db.Store, cfg db.ServiceView, runDir string) (*SystemdService, error) {
	return &SystemdService{db: db, cfg: cfg, runDir: runDir, flatRuntimeArtifacts: true}, nil
}

// NewDockerComposeService creates a new docker compose service from a config.
func NewDockerComposeService(db *db.Store, cfg db.ServiceView, dataDir, runDir string, options ...SystemdServiceOption) (*DockerComposeService, error) {
	sd, err := NewSystemdService(db, cfg, runDir, options...)
	if err != nil {
		return nil, err
	}
	return &DockerComposeService{
		Name:          cfg.Name(),
		cfg:           cfg.AsStruct(),
		DataDir:       dataDir,
		NewCmd:        cmdutil.NewStdCmd,
		NewCmdContext: cmdutil.NewStdCmdContext,
		sd:            sd,
	}, nil
}
