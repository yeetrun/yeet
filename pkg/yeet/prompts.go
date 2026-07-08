// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"os"
	"strings"

	"github.com/yeetrun/yeet/pkg/cmdutil"
)

type workspacePromptChoice int

const (
	workspacePromptUseCurrent workspacePromptChoice = iota
	workspacePromptUseKnown
	workspacePromptRunOnce
)

type workspaceSelection struct {
	Choice workspacePromptChoice
	Path   string
}

type yeetPrompter interface {
	Confirm(msg string, def bool) (bool, error)
	SelectWorkspace(host string, paths []string, current string) (workspaceSelection, error)
	SelectDefaultHost(hosts []string, current string) (string, error)
	Input(msg string, def string) (string, error)
	Secret(msg string) (string, error)
}

var activePrompter yeetPrompter = newDefaultPrompter()

type plainPrompter struct{}

func (plainPrompter) Confirm(msg string, def bool) (bool, error) {
	return cmdutil.Confirm(os.Stdin, os.Stdout, msg)
}

func (plainPrompter) SelectWorkspace(host string, paths []string, current string) (workspaceSelection, error) {
	if len(paths) == 1 {
		ok, err := plainPrompter{}.Confirm(fmt.Sprintf("No workspace is associated with %s. Use %s for %s?", host, paths[0], host), true)
		if err != nil || !ok {
			return workspaceSelection{Choice: workspacePromptRunOnce}, err
		}
		return workspaceSelection{Choice: workspacePromptUseKnown, Path: paths[0]}, nil
	}
	ok, err := plainPrompter{}.Confirm(fmt.Sprintf("Use %s as a yeet workspace?", current), true)
	if err != nil || !ok {
		return workspaceSelection{Choice: workspacePromptRunOnce}, err
	}
	return workspaceSelection{Choice: workspacePromptUseCurrent, Path: current}, nil
}

func (plainPrompter) SelectDefaultHost(hosts []string, current string) (string, error) {
	if len(hosts) == 0 {
		return "", nil
	}
	current = defaultDefaultHost(current)
	if _, err := fmt.Fprintf(os.Stdout, "Default catch host (%s, blank to keep %s): ", strings.Join(hosts, "/"), current); err != nil {
		return "", err
	}
	var value string
	if _, err := fmt.Fscanln(os.Stdin, &value); err != nil && err.Error() != "unexpected newline" {
		return "", err
	}
	return selectedDefaultHost(hosts, current, value)
}

func defaultDefaultHost(current string) string {
	current = normalizeCatchHost(current)
	if current == "" {
		return defaultCatchHost
	}
	return current
}

func selectedDefaultHost(hosts []string, current string, value string) (string, error) {
	value = normalizeCatchHost(value)
	if value == "" || value == current {
		return "", nil
	}
	for _, host := range hosts {
		if value == normalizeCatchHost(host) {
			return value, nil
		}
	}
	return "", fmt.Errorf("default catch host must be one of: %s", strings.Join(hosts, ", "))
}

func (plainPrompter) Input(msg string, def string) (string, error) {
	if _, err := fmt.Fprintf(os.Stdout, "%s [%s]: ", msg, def); err != nil {
		return "", err
	}
	var value string
	if _, err := fmt.Fscanln(os.Stdin, &value); err != nil && err.Error() != "unexpected newline" {
		return "", err
	}
	if value == "" {
		return def, nil
	}
	return value, nil
}

func (plainPrompter) Secret(msg string) (string, error) {
	if _, err := fmt.Fprintf(os.Stdout, "%s: ", msg); err != nil {
		return "", err
	}
	var value string
	if _, err := fmt.Fscanln(os.Stdin, &value); err != nil {
		return "", err
	}
	return value, nil
}
