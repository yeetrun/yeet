// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func finishInitWorkspaceSetup(ui *initUI, opts initOptions) error {
	if opts.noWorkspace {
		if !opts.suppressNextSteps {
			printInitNextSteps(ui, "")
		}
		return nil
	}
	workspace := strings.TrimSpace(opts.workspace)
	if workspace == "" {
		if !canPromptForWorkspace() {
			printInitNextSteps(ui, "")
			return nil
		}
		value, err := activePrompter.Input("Service workspace", initWorkspaceDefault())
		if err != nil {
			return fmt.Errorf("catch installed successfully, but local workspace setup failed: %w", err)
		}
		workspace = value
	}
	loc, err := seedWorkspaceConfig(workspace, Host())
	if err != nil {
		return fmt.Errorf("catch installed successfully, but local workspace setup failed: %w", err)
	}
	setDefaultHost(Host())
	if err := addWorkspacePath(loc.Dir); err != nil {
		return fmt.Errorf("catch installed successfully, but local workspace setup failed: %w", err)
	}
	if err := saveClientConfig(); err != nil {
		return fmt.Errorf("catch installed successfully, but local workspace setup failed: %w", err)
	}
	printInitNextSteps(ui, loc.Dir)
	return nil
}

func initWorkspaceDefault() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("~", "yeet-services")
	}
	return filepath.Join(home, "yeet-services")
}

func printInitNextSteps(ui *initUI, workspace string) {
	if strings.TrimSpace(workspace) != "" {
		ui.Info(fmt.Sprintf("Next: cd %s", workspace))
		ui.Info("Try: yeet run -p 18080:80 hello nginx:alpine")
		return
	}
	ui.Info("Try: yeet run -p 18080:80 hello nginx:alpine")
	ui.Info("Without a workspace, this run will not save client-side service config. Set one with: yeet config --workspace PATH")
}
