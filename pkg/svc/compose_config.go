// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ComposeResolveOptions identifies the exact Compose files and project context
// used to resolve a canonical Docker Compose application model.
type ComposeResolveOptions struct {
	ProjectName string
	ProjectDir  string
	Files       []string
	NewCmd      func(context.Context, string, ...string) *exec.Cmd
}

// ResolveComposeJSON asks Docker Compose v2 for its canonical JSON application
// model. Callers must admit the returned model before using it at a security
// boundary.
func ResolveComposeJSON(ctx context.Context, opts ComposeResolveOptions) ([]byte, error) {
	if len(opts.Files) == 0 {
		return nil, fmt.Errorf("resolve Docker Compose application model: explicit compose files are required")
	}
	args := []string{"compose", "--project-name", opts.ProjectName, "--project-directory", opts.ProjectDir}
	for _, file := range opts.Files {
		if strings.TrimSpace(file) == "" {
			return nil, fmt.Errorf("resolve Docker Compose application model: compose files cannot contain an empty path")
		}
		args = append(args, "--file", file)
	}
	args = append(args, "config", "--format", "json")

	docker, err := DockerCmd()
	if err != nil {
		return nil, err
	}
	newCmd := opts.NewCmd
	if newCmd == nil {
		newCmd = exec.CommandContext
	}
	cmd := newCmd(ctx, docker, args...)
	cmd.Dir = opts.ProjectDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("resolve Docker Compose application model: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
