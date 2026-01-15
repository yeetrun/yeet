// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
)

const (
	editUnitsSeparator = "=====================================|%s|====================================="
)

var (
	editUnitsSeparatorRe = regexp.MustCompile(`=====================================\|([^|]+)\|=====================================`)
)

func (e *ttyExecer) editCmdFunc(flags cli.EditFlags) error {
	st, err := e.s.serviceType(e.sn)
	if err != nil {
		return err
	}

	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return err
	}
	editConfig := flags.Config

	var srcPath string

	editConfigFn := func(cfg any) error {
		bs, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal systemd config: %w", err)
		}
		srcf, err := createTmpFile()
		if err != nil {
			return fmt.Errorf("failed to create temp file: %w", err)
		}
		defer srcf.Close()
		srcPath = srcf.Name()
		if _, err := io.Copy(srcf, bytes.NewReader(bs)); err != nil {
			return fmt.Errorf("failed to write to temp file: %w", err)
		}
		return nil
	}

	var systemdUnitsBeingEdited []string
	af := sv.AsStruct().Artifacts
	if editConfig {
		if err := editConfigFn(sv); err != nil {
			return fmt.Errorf("failed to edit config: %w", err)
		}
	} else {
		switch st {
		case db.ServiceTypeDockerCompose:
			srcPath, _ = af.Latest(db.ArtifactDockerComposeFile)
		case db.ServiceTypeSystemd:
			if len(af) == 0 {
				return fmt.Errorf("no unit files found")
			}
			srcf, err := createTmpFile()
			if err != nil {
				return fmt.Errorf("failed to create temp file: %w", err)
			}
			defer srcf.Close()

			count := 0
			for _, name := range []db.ArtifactName{db.ArtifactSystemdUnit, db.ArtifactSystemdTimerFile} {
				path, ok := af.Latest(name)
				if !ok {
					continue
				}
				if count > 0 {
					fmt.Fprintf(srcf, "\n\n")
				}
				fmt.Fprintf(srcf, editUnitsSeparator, name)
				fmt.Fprintf(srcf, "\n\n")
				systemdUnitsBeingEdited = append(systemdUnitsBeingEdited, path)
				f, err := os.Open(path)
				if err != nil {
					return fmt.Errorf("failed to open unit file: %w", err)
				}
				if _, err := io.Copy(srcf, f); err != nil {
					return fmt.Errorf("failed to write to temp file: %w", err)
				}
				count++
			}
			if err := srcf.Close(); err != nil {
				return fmt.Errorf("failed to close temp file: %w", err)
			}
			srcPath = srcf.Name()
		}
	}

	tmpPath, err := copyToTmpFile(srcPath)
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath)

	if err := e.editFile(tmpPath); err != nil {
		return fmt.Errorf("failed to edit file: %w", err)
	}

	if same, err := fileutil.Identical(srcPath, tmpPath); err != nil {
		return err
	} else if same {
		e.printf("No changes detected\n")
		return nil
	}

	if editConfig {
		bs, err := os.ReadFile(tmpPath)
		if err != nil {
			return fmt.Errorf("failed to read temp file: %w", err)
		}
		var s2 db.Service
		if err := json.Unmarshal(bs, &s2); err != nil {
			return fmt.Errorf("failed to unmarshal temp file: %w", err)
		}
		_, _, err = e.s.cfg.DB.MutateService(e.sn, func(d *db.Data, s *db.Service) error {
			*s = s2
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to update service: %w", err)
		}
		i, err := e.s.NewInstaller(e.installerCfg())
		if err != nil {
			return fmt.Errorf("failed to create installer: %w", err)
		}
		i.NewCmd = e.newCmd
		return i.InstallGen(s2.Generation)
	}

	installFile := func() error {
		f, err := os.Open(tmpPath)
		if err != nil {
			return fmt.Errorf("failed to open temp file: %w", err)
		}
		defer f.Close()
		icfg := e.fileInstaller(netFlags{}, nil)
		fi, err := NewFileInstaller(e.s, icfg)
		if err != nil {
			return fmt.Errorf("failed to create installer: %w", err)
		}
		defer fi.Close()
		if _, err := io.Copy(fi, f); err != nil {
			fi.Fail()
			return fmt.Errorf("failed to copy temp file to installer: %w", err)
		}
		return fi.Close()
	}

	switch st {
	case db.ServiceTypeDockerCompose:
		if editConfig {
			return fmt.Errorf("not implemented")
		}
		return installFile()
	case db.ServiceTypeSystemd:
		if editConfig {
			return fmt.Errorf("not implemented")
		}
		bs, err := os.ReadFile(tmpPath)
		if err != nil {
			return fmt.Errorf("failed to read temp file: %w", err)
		}
		submatches := editUnitsSeparatorRe.FindAllSubmatch(bs, -1)
		separateContents := editUnitsSeparatorRe.Split(string(bs), -1)
		if len(separateContents) < 1 {
			return fmt.Errorf("no unit files found")
		}
		separateContents = separateContents[1:] // Skip the first split which is empty
		if len(separateContents) != len(systemdUnitsBeingEdited) || len(submatches) != len(systemdUnitsBeingEdited) {
			return fmt.Errorf("mismatched number of unit files and contents")
		}
		newArtifacts := make(map[db.ArtifactName]string)
		for i, content := range separateContents {
			name := string(submatches[i][1])
			content = strings.TrimSpace(content)
			tmpf, err := createTmpFile()
			if err != nil {
				return fmt.Errorf("failed to create temp file: %w", err)
			}
			defer os.Remove(tmpf.Name())
			defer tmpf.Close()
			if _, err := tmpf.WriteString(content); err != nil {
				return fmt.Errorf("failed to write to temp file: %w", err)
			}
			if err := tmpf.Close(); err != nil {
				return fmt.Errorf("failed to close temp file: %w", err)
			}
			p, ok := af.Latest(db.ArtifactName(name))
			if !ok {
				return fmt.Errorf("no unit file found for %q", name)
			}
			binPath := fileutil.UpdateVersion(p)
			if err := fileutil.CopyFile(tmpf.Name(), binPath); err != nil {
				return fmt.Errorf("failed to copy temp file to binary path: %w", err)
			}
			newArtifacts[db.ArtifactName(name)] = binPath
		}
		_, _, err = e.s.cfg.DB.MutateService(e.sn, func(d *db.Data, s *db.Service) error {
			for name, path := range newArtifacts {
				s.Artifacts[name].Refs["staged"] = path
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to update artifacts: %w", err)
		}
		i, err := e.s.NewInstaller(e.installerCfg())
		if err != nil {
			return fmt.Errorf("failed to create installer: %w", err)
		}
		i.NewCmd = e.newCmd
		return i.Install()
	default:
		return fmt.Errorf("unsupported service type: %v", st)
	}
}

func createTmpFile() (*os.File, error) {
	return os.CreateTemp("", "catch-tmp-*")
}

func copyToTmpFile(src string) (string, error) {
	tmpf, err := createTmpFile()
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	if src != "" {
		if err := fileutil.CopyFile(src, tmpf.Name()); err != nil {
			return "", fmt.Errorf("failed to copy file: %w", err)
		}
	}
	tmpf.Close()
	return tmpf.Name(), nil
}

func (e *ttyExecer) editFile(path string) error {
	if !e.isPty {
		return fmt.Errorf("edit requires a pty, please run with a TTY")
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	cmd := e.newCmd(editor, path)
	term := e.ptyReq.Term
	if term == "" {
		term = "xterm"
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", term))
	return cmd.Run()
}
