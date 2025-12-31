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

// NewSystemdService creates a new systemd service from a SystemdConfigView.
func NewSystemdService(db *db.Store, cfg db.ServiceView, runDir string) (*SystemdService, error) {
	return &SystemdService{db: db, cfg: cfg, runDir: runDir}, nil
}

// NewDockerComposeService creates a new docker compose service from a config.
func NewDockerComposeService(db *db.Store, cfg db.ServiceView, dataDir, runDir string) (*DockerComposeService, error) {
	sd, err := NewSystemdService(db, cfg, runDir)
	if err != nil {
		return nil, err
	}
	return &DockerComposeService{
		Name:    cfg.Name(),
		cfg:     cfg.AsStruct(),
		DataDir: dataDir,
		NewCmd:  cmdutil.NewStdCmd,
		sd:      sd,
	}, nil
}
