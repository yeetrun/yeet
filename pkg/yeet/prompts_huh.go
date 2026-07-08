// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"

	"github.com/charmbracelet/huh"
)

type huhPrompter struct{}

func newDefaultPrompter() yeetPrompter {
	return huhPrompter{}
}

func (huhPrompter) Confirm(msg string, def bool) (bool, error) {
	value := def
	form := huh.NewConfirm().
		Title(msg).
		Value(&value)
	if err := form.Run(); err != nil {
		return false, err
	}
	return value, nil
}

func (huhPrompter) Input(msg string, def string) (string, error) {
	value := def
	form := huh.NewInput().
		Title(msg).
		Value(&value).
		Placeholder(def)
	if err := form.Run(); err != nil {
		return "", err
	}
	if value == "" {
		return def, nil
	}
	return value, nil
}

func (huhPrompter) Secret(msg string) (string, error) {
	var value string
	form := huh.NewInput().
		Title(msg).
		EchoMode(huh.EchoModePassword).
		Value(&value)
	if err := form.Run(); err != nil {
		return "", err
	}
	return value, nil
}

func (huhPrompter) SelectWorkspace(host string, paths []string, current string) (workspaceSelection, error) {
	options := make([]huh.Option[workspaceSelection], 0, len(paths)+2)
	for _, path := range paths {
		options = append(options, huh.NewOption(path, workspaceSelection{
			Choice: workspacePromptUseKnown,
			Path:   path,
		}))
	}
	options = append(options,
		huh.NewOption(fmt.Sprintf("%s (current directory)", current), workspaceSelection{
			Choice: workspacePromptUseCurrent,
			Path:   current,
		}),
		huh.NewOption("Run once without saving", workspaceSelection{
			Choice: workspacePromptRunOnce,
		}),
	)
	var selected workspaceSelection
	form := huh.NewSelect[workspaceSelection]().
		Title(fmt.Sprintf("No workspace is associated with %s. Choose a workspace.", host)).
		Options(options...).
		Value(&selected)
	if err := form.Run(); err != nil {
		return workspaceSelection{}, err
	}
	return selected, nil
}
