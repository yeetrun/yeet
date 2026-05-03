// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"errors"
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

type editSource struct {
	path         string
	cleanup      func() error
	systemdUnits []db.ArtifactName
}

type editCommandSpec struct {
	name string
	args []string
	term string
}

type systemdEditUnit struct {
	name    db.ArtifactName
	content string
}

type editSession struct {
	serviceType db.ServiceType
	service     db.ServiceView
	source      editSource
	tmpPath     string
	config      bool
}

func (s *editSession) cleanupInto(retErr *error) {
	s.source.cleanupInto(retErr)
	cleanupFileInto(s.tmpPath, retErr)
}

func (e *ttyExecer) editCmdFunc(flags cli.EditFlags) (retErr error) {
	session, err := e.newEditSession(flags)
	if err != nil {
		return err
	}
	defer session.cleanupInto(&retErr)

	changed, err := e.runEditSession(session)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return e.applyEditSession(session)
}

func (e *ttyExecer) newEditSession(flags cli.EditFlags) (*editSession, error) {
	st, err := e.s.serviceType(e.sn)
	if err != nil {
		return nil, err
	}

	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return nil, err
	}
	source, err := prepareEditSource(sv, st, flags.Config)
	if err != nil {
		return nil, err
	}

	tmpPath, err := copyToTmpFile(source.path)
	if err != nil {
		source.cleanupInto(&err)
		return nil, err
	}
	return &editSession{
		serviceType: st,
		service:     sv,
		source:      source,
		tmpPath:     tmpPath,
		config:      flags.Config,
	}, nil
}

func (e *ttyExecer) runEditSession(session *editSession) (bool, error) {
	if err := e.editFile(session.tmpPath); err != nil {
		return false, fmt.Errorf("failed to edit file: %w", err)
	}

	if same, err := fileutil.Identical(session.source.path, session.tmpPath); err != nil {
		return false, err
	} else if same {
		e.printf("No changes detected\n")
		return false, nil
	}
	return true, nil
}

func (e *ttyExecer) applyEditSession(session *editSession) error {
	if session.config {
		return e.applyEditedConfig(session.tmpPath)
	}

	switch session.serviceType {
	case db.ServiceTypeDockerCompose:
		return e.installEditedFile(session.tmpPath)
	case db.ServiceTypeSystemd:
		return e.applyEditedSystemd(session.tmpPath, session.service.AsStruct().Artifacts, len(session.source.systemdUnits))
	default:
		return fmt.Errorf("unsupported service type: %v", session.serviceType)
	}
}

func prepareEditSource(sv db.ServiceView, st db.ServiceType, editConfig bool) (editSource, error) {
	if editConfig {
		source, err := serviceConfigEditSource(sv)
		if err != nil {
			return editSource{}, fmt.Errorf("failed to edit config: %w", err)
		}
		return source, nil
	}

	af := sv.AsStruct().Artifacts
	switch st {
	case db.ServiceTypeDockerCompose:
		srcPath, _ := af.Latest(db.ArtifactDockerComposeFile)
		return editSource{path: srcPath}, nil
	case db.ServiceTypeSystemd:
		return systemdEditSource(af)
	default:
		return editSource{}, nil
	}
}

func serviceConfigEditSource(cfg any) (editSource, error) {
	bs, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return editSource{}, fmt.Errorf("failed to marshal systemd config: %w", err)
	}
	return newTempEditSource(func(w io.Writer) error {
		if _, err := w.Write(bs); err != nil {
			return fmt.Errorf("failed to write to temp file: %w", err)
		}
		return nil
	})
}

func systemdEditSource(af db.ArtifactStore) (editSource, error) {
	if len(af) == 0 {
		return editSource{}, fmt.Errorf("no unit files found")
	}

	var names []db.ArtifactName
	source, err := newTempEditSource(func(w io.Writer) error {
		var err error
		names, err = writeSystemdEditSource(w, af)
		return err
	})
	if err != nil {
		return editSource{}, err
	}
	source.systemdUnits = names
	return source, nil
}

func writeSystemdEditSource(w io.Writer, af db.ArtifactStore) ([]db.ArtifactName, error) {
	names := make([]db.ArtifactName, 0, 2)
	for _, name := range []db.ArtifactName{db.ArtifactSystemdUnit, db.ArtifactSystemdTimerFile} {
		path, ok := af.Latest(name)
		if !ok {
			continue
		}
		if len(names) > 0 {
			if _, err := io.WriteString(w, "\n\n"); err != nil {
				return nil, fmt.Errorf("failed to write to temp file: %w", err)
			}
		}
		if _, err := fmt.Fprintf(w, editUnitsSeparator, name); err != nil {
			return nil, fmt.Errorf("failed to write to temp file: %w", err)
		}
		if _, err := io.WriteString(w, "\n\n"); err != nil {
			return nil, fmt.Errorf("failed to write to temp file: %w", err)
		}
		if err := copyPathToWriter(w, path); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

func copyPathToWriter(w io.Writer, path string) (retErr error) {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open unit file: %w", err)
	}
	defer closeInto(f, "unit file", &retErr)

	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	return nil
}

func (e *ttyExecer) applyEditedConfig(tmpPath string) error {
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
	return e.installServiceGeneration(e.installerCfg(), s2.Generation)
}

func (e *ttyExecer) installEditedFile(tmpPath string) (retErr error) {
	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to open temp file: %w", err)
	}
	defer closeInto(f, "temp file", &retErr)

	icfg := e.fileInstaller(netFlags{}, nil)
	fi, err := NewFileInstaller(e.s, icfg)
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	if _, err := io.Copy(fi, f); err != nil {
		fi.Fail()
		return errors.Join(fmt.Errorf("failed to copy temp file to installer: %w", err), fi.Close())
	}
	return fi.Close()
}

func (e *ttyExecer) applyEditedSystemd(tmpPath string, af db.ArtifactStore, expectedUnits int) error {
	units, err := readEditedSystemdUnits(tmpPath, expectedUnits)
	if err != nil {
		return err
	}
	newArtifacts, err := stageEditedSystemdUnits(af, units)
	if err != nil {
		return err
	}
	if err := e.stageEditedArtifacts(newArtifacts); err != nil {
		return err
	}
	return e.installEditedSystemd()
}

func readEditedSystemdUnits(tmpPath string, expectedUnits int) ([]systemdEditUnit, error) {
	bs, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read temp file: %w", err)
	}
	return parseEditedSystemdUnits(bs, expectedUnits)
}

func (e *ttyExecer) installEditedSystemd() error {
	return e.installService(e.installerCfg())
}

func parseEditedSystemdUnits(bs []byte, expectedUnits int) ([]systemdEditUnit, error) {
	submatches := editUnitsSeparatorRe.FindAllSubmatch(bs, -1)
	separateContents := editUnitsSeparatorRe.Split(string(bs), -1)
	if len(separateContents) < 1 {
		return nil, fmt.Errorf("no unit files found")
	}
	separateContents = separateContents[1:] // Skip the first split which is empty.
	if len(separateContents) != expectedUnits || len(submatches) != expectedUnits {
		return nil, fmt.Errorf("mismatched number of unit files and contents")
	}

	units := make([]systemdEditUnit, 0, len(separateContents))
	for i, content := range separateContents {
		units = append(units, systemdEditUnit{
			name:    db.ArtifactName(string(submatches[i][1])),
			content: strings.TrimSpace(content),
		})
	}
	return units, nil
}

func stageEditedSystemdUnits(af db.ArtifactStore, units []systemdEditUnit) (map[db.ArtifactName]string, error) {
	newArtifacts := make(map[db.ArtifactName]string, len(units))
	for _, unit := range units {
		p, ok := af.Latest(unit.name)
		if !ok {
			return nil, fmt.Errorf("no unit file found for %q", unit.name)
		}
		binPath, err := stageEditedSystemdUnit(p, unit.content)
		if err != nil {
			return nil, err
		}
		newArtifacts[unit.name] = binPath
	}
	return newArtifacts, nil
}

func stageEditedSystemdUnit(currentPath, content string) (binPath string, retErr error) {
	source, err := newTempEditSource(func(w io.Writer) error {
		if _, err := io.WriteString(w, content); err != nil {
			return fmt.Errorf("failed to write to temp file: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	defer source.cleanupInto(&retErr)

	binPath = fileutil.UpdateVersion(currentPath)
	if err := fileutil.CopyFile(source.path, binPath); err != nil {
		return "", fmt.Errorf("failed to copy temp file to binary path: %w", err)
	}
	return binPath, nil
}

func (e *ttyExecer) stageEditedArtifacts(newArtifacts map[db.ArtifactName]string) error {
	_, _, err := e.s.cfg.DB.MutateService(e.sn, func(d *db.Data, s *db.Service) error {
		for name, path := range newArtifacts {
			if err := stageEditedArtifact(s, name, path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update artifacts: %w", err)
	}
	return nil
}

func stageEditedArtifact(s *db.Service, name db.ArtifactName, path string) error {
	artifact, ok := s.Artifacts[name]
	if !ok || artifact == nil {
		return fmt.Errorf("no artifact found for %q", name)
	}
	if artifact.Refs == nil {
		artifact.Refs = map[db.ArtifactRef]string{}
	}
	artifact.Refs["staged"] = path
	return nil
}

func createTmpFile() (*os.File, error) {
	return os.CreateTemp("", "catch-tmp-*")
}

func copyToTmpFile(src string) (string, error) {
	tmpPath, err := createClosedTmpFile()
	if err != nil {
		return "", err
	}
	if src == "" {
		return tmpPath, nil
	}
	if err := fileutil.CopyFile(src, tmpPath); err != nil {
		return "", errors.Join(fmt.Errorf("failed to copy file: %w", err), removeFile(tmpPath))
	}
	return tmpPath, nil
}

func createClosedTmpFile() (tmpPath string, retErr error) {
	tmpf, err := createTmpFile()
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath = tmpf.Name()
	if err := tmpf.Close(); err != nil {
		return "", errors.Join(fmt.Errorf("failed to close temp file: %w", err), removeFile(tmpPath))
	}
	return tmpPath, nil
}

func (e *ttyExecer) editFile(path string) error {
	if e.editFileFunc != nil {
		return e.editFileFunc(path)
	}
	if !e.isPty {
		return fmt.Errorf("edit requires a pty, please run with a TTY")
	}

	spec := resolveEditCommand(os.Getenv("EDITOR"), e.ptyReq.Term, path)
	cmd := e.newCmd(spec.name, spec.args...)
	cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", spec.term))
	return cmd.Run()
}

func resolveEditCommand(editor, term, path string) editCommandSpec {
	if editor == "" {
		editor = "vim"
	}
	if term == "" {
		term = "xterm"
	}
	return editCommandSpec{
		name: editor,
		args: []string{path},
		term: term,
	}
}

func newTempEditSource(write func(io.Writer) error) (source editSource, retErr error) {
	f, err := createTmpFile()
	if err != nil {
		return editSource{}, fmt.Errorf("failed to create temp file: %w", err)
	}
	path := f.Name()
	closed := false
	defer func() {
		if !closed {
			closeInto(f, "temp file", &retErr)
		}
		if retErr != nil {
			retErr = errors.Join(retErr, removeFile(path))
		}
	}()

	if err := write(f); err != nil {
		return editSource{}, err
	}
	closed = true
	if err := f.Close(); err != nil {
		return editSource{}, fmt.Errorf("failed to close temp file: %w", err)
	}
	return editSource{
		path: path,
		cleanup: func() error {
			return removeFile(path)
		},
	}, nil
}

func (s editSource) cleanupInto(retErr *error) {
	if s.cleanup == nil {
		return
	}
	*retErr = errors.Join(*retErr, s.cleanup())
}

func cleanupFileInto(path string, retErr *error) {
	*retErr = errors.Join(*retErr, removeFile(path))
}

func removeFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func closeInto(c io.Closer, name string, retErr *error) {
	if err := c.Close(); err != nil {
		*retErr = errors.Join(*retErr, fmt.Errorf("failed to close %s: %w", name, err))
	}
}
