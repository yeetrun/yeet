// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"golang.org/x/sys/unix"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/logtail/backoff"
)

type Status string

type InstallTargetState struct {
	Path    string
	Present bool
	Mode    os.FileMode
	UID     uint32
	GID     uint32
	Nlink   uint64
	Size    int64
	SHA256  string
}

var (
	systemdInstallTargetFlistxattr = unix.Flistxattr
	systemdInstallArtifactChown    = (*os.File).Chown
)

const (
	StatusRunning Status = "Running"
	StatusStopped Status = "Stopped"
	StatusUnknown Status = "Unknown"
)

// TimerConfig provides the setup for a Timer. The OnCalendar field is required.
type TimerConfig struct {
	Description string `json:",omitempty"` // Description of the timer.
	OnCalendar  string // Run on a calendar event.
	Persistent  bool   // Ensures missed timer events run after system resumes from downtime.
}

const (
	systemdServiceTemplate = `[Unit]
{{with .Description}}Description={{.}}
{{end}}ConditionFileIsExecutable={{.ConditionExecutable}}
{{if .Wants}}Wants={{.Wants}}{{end}}
{{if .Requires}}Requires={{.Requires}}{{end}}
{{if .Before}}Before={{.Before}}{{end}}
{{if .After}}After={{.After}}{{else if .Requires}}After={{.Requires}}{{end}}
{{if .PartOf}}PartOf={{.PartOf}}{{end}}

[Service]
{{range .ExecStartPre}}ExecStartPre={{.}}{{end}}
ExecStart={{.Executable}}{{range .Arguments}} {{.}}{{end}}
{{range .ExecStartPost}}ExecStartPost={{.}}{{end}}
{{if or .OneShot .Timer}}Type=oneshot{{end}}
{{if .WorkingDirectory}}WorkingDirectory={{.WorkingDirectory}}{{end}}
{{if .Restart}}Restart={{.Restart}}{{end}}
RestartSec=1
RestartSteps=10
RestartMaxDelaySec=60
{{if .User}}User={{.User}}{{end}}
{{if .Group}}Group={{.Group}}{{end}}
{{if .EnvFile}}EnvironmentFile={{.EnvFile}}{{end}}
{{if and .User .WorkingDirectory}}Environment=HOME={{.WorkingDirectory}} USER={{.User}} LOGNAME={{.User}} SHELL=/bin/sh{{end}}
{{if .NetNS}}NetworkNamespacePath=/var/run/netns/{{.NetNS}}{{end}}
{{if .OneShot}}RemainAfterExit=yes{{end}}
{{if .StopCmd}}ExecStop={{.StopCmd}}{{end}}
{{if .ResolvConf}}
BindReadOnlyPaths={{.ResolvConf}}:/etc/resolv.conf
{{end}}
{{if or .ResolvConf .PrivateMounts}}
PrivateMounts=yes
{{end}}
[Install]
WantedBy={{.WantedBy}}
`
	systemdTimerTemplate = `[Unit]

[Timer]
OnCalendar={{.OnCalendar}}
Persistent={{.Persistent}}

[Install]
WantedBy=timers.target
`
)

var (
	monitorTailscaleFn = func(s *SystemdService) error {
		return s.monitorTailscale()
	}

	systemdServiceTmpl = template.Must(template.New("systemdService").Parse(systemdServiceTemplate))
	systemdTimerTmpl   = template.Must(template.New("systemdTimer").Parse(systemdTimerTemplate))
)

const tailscaleReadyTimeout = 30 * time.Second

const catchSystemServiceName = "catch"

type SystemdUnit struct {
	Name string // Required name of the service. No spaces suggested.

	// Description is the human-readable [Unit] description.
	Description string

	// User is the user to run the service as.
	User string

	// Group is the primary group to run the service as.
	Group string

	// Executable is the path to the executable to run or the command to run.
	Executable string

	// ConditionExecutable overrides ConditionFileIsExecutable while ExecStart
	// still uses Executable. Empty preserves the historical behavior.
	ConditionExecutable string

	// Arguments are the arguments to pass to the service.
	Arguments []string

	// OneShot, when true, will run the service as a oneshot service.
	OneShot bool

	// StopCmd is the command to run to stop the service.
	StopCmd string

	// Timer, when set, will defer running of the service to a separate timer
	// unit. This is used for `cron` like functionality. If Timer is nil, the
	// service is configured normally.
	Timer *TimerConfig

	// EnvFile is the path to an environment file.
	EnvFile string

	// WorkingDirectory is the working directory for the service.
	WorkingDirectory string

	// NetNS is the network namespace the service is in.
	// If empty, the service is on the host network.
	NetNS string

	// Requires is a list of services that this service requires to run.
	// For multiple services, separate with spaces.
	Requires string

	// Wants is a weaker dependency list than Requires.
	// For multiple services, separate with spaces.
	Wants string

	// After controls service ordering. If empty, Requires is used to preserve
	// the historical "requires also means after" behavior of this generator.
	After string

	// PartOf couples this unit's lifecycle to another unit. When the parent
	// unit is restarted or stopped, systemd propagates that operation here.
	PartOf string

	// Before controls reverse service ordering.
	Before string

	// ExecStartPre commands run before ExecStart.
	ExecStartPre []string

	// ExecStartPost commands run after ExecStart and participate in systemd
	// ordering constraints.
	ExecStartPost []string

	// WantedBy controls the [Install] target list. If empty, multi-user.target
	// is used.
	WantedBy string

	// ResolvConf is the path to the resolv.conf file to use.
	ResolvConf string

	// PrivateMounts gives the service its own mount namespace without adding
	// a generated bind mount.
	PrivateMounts bool
}

// NewISONetworkUnit renders the local-only, fail-closed network gate for an
// isolated service. Artifact staging and activation are owned by the service
// installer transaction; this helper only defines the deterministic unit.
func NewISONetworkUnit(service, catchBin, dataDir string) (*SystemdUnit, error) {
	service = strings.TrimSpace(service)
	catchBin = strings.TrimSpace(catchBin)
	dataDir = strings.TrimSpace(dataDir)
	if service == "" || strings.ContainsAny(service, "/\\ \t\r\n") {
		return nil, fmt.Errorf("invalid ISO service name %q", service)
	}
	if catchBin == "" || dataDir == "" {
		return nil, fmt.Errorf("ISO network unit requires catch and data-dir paths")
	}
	if strings.ContainsAny(catchBin+dataDir, " \t\r\n") {
		return nil, fmt.Errorf("ISO network unit paths cannot contain whitespace")
	}
	return &SystemdUnit{
		Name:        "yeet-" + service + "-ns",
		Description: "yeet ISO network for " + service,
		Executable:  catchBin,
		Arguments:   []string{"-data-dir", dataDir, "iso-network-ensure", service},
		StopCmd:     catchBin + " -data-dir " + dataDir + " iso-network-clean " + service,
		Before:      service + ".service",
		After:       "network-online.target docker.service",
		Wants:       "network-online.target",
		OneShot:     true,
	}, nil
}

func (u *SystemdUnit) serviceUnit() string {
	return u.Name + ".service"
}

func (u *SystemdUnit) timerUnit() string {
	return u.Name + ".timer"
}

func (u *SystemdUnit) WriteOutUnitFiles(root string) (map[db.ArtifactName]string, error) {
	servicePath := filepath.Join(root, fileutil.ApplyVersion(u.serviceUnit()))
	if err := u.writeOutService(servicePath); err != nil {
		return nil, err
	}
	paths := map[db.ArtifactName]string{
		db.ArtifactSystemdUnit: servicePath,
	}

	if u.Timer != nil {
		timerPath := filepath.Join(root, fileutil.ApplyVersion(u.timerUnit()))
		if err := u.writeOutTimer(timerPath); err != nil {
			return nil, err
		}
		paths[db.ArtifactSystemdTimerFile] = timerPath
	}

	return paths, nil
}

func (u *SystemdUnit) writeOutService(path string) (err error) {
	// Timer units do not support "always" or "on-success" restarts
	restartDefault := "always"
	if u.Timer != nil || u.OneShot {
		restartDefault = "on-failure"
	}
	wantedBy := u.WantedBy
	if wantedBy == "" {
		wantedBy = "multi-user.target"
	}
	conditionExecutable := u.ConditionExecutable
	if conditionExecutable == "" {
		conditionExecutable = u.Executable
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer closeFile(f, &err)
	return systemdServiceTmpl.Execute(f, struct {
		*SystemdUnit
		Restart             string
		WantedBy            string
		ConditionExecutable string
	}{
		u,
		restartDefault,
		wantedBy,
		conditionExecutable,
	})
}

func (u *SystemdUnit) writeOutTimer(path string) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer closeFile(f, &err)
	return systemdTimerTmpl.Execute(f, u.Timer)
}

func closeFile(f *os.File, err *error) {
	if closeErr := f.Close(); closeErr != nil && *err == nil {
		*err = closeErr
	}
}

type SystemdService struct {
	db                   *db.Store
	cfg                  db.ServiceView
	runDir               string
	systemdDir           string
	flatRuntimeArtifacts bool
	tailscaleGuardRunner string
}

func (s *SystemdService) Name() string {
	return s.cfg.Name()
}

func (s *SystemdService) run(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	if out, err := cmd.Output(); err != nil {
		return fmt.Errorf("failed to run systemctl %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return nil
}

type artifactInstall struct {
	dstPath string
	unit    string

	primaryUnitIfAvailable bool
}

type installStep struct {
	artifact db.ArtifactName
	artifactInstall
}

func (s *SystemdService) artifactInstaller() map[db.ArtifactName]artifactInstall {
	root := filepath.Dir(s.runDir)
	binDir := filepath.Join(root, "bin")
	envDir := filepath.Join(root, "env")
	if s.flatRuntimeArtifacts {
		binDir = s.runDir
		envDir = s.runDir
	}
	installers := map[db.ArtifactName]artifactInstall{
		db.ArtifactSystemdUnit:      {dstPath: s.servicePath(), unit: s.serviceUnit()},
		db.ArtifactSystemdTimerFile: {dstPath: s.timerPath(), unit: s.timerUnit(), primaryUnitIfAvailable: true},

		db.ArtifactNetNSService: {dstPath: s.netnsServicePath(), unit: s.netnsServiceUnit()},
		db.ArtifactNetNSEnv:     {dstPath: filepath.Join(envDir, "netns.env")},

		db.ArtifactTypeScriptFile: {dstPath: filepath.Join(s.runDir, "main.ts")},
		db.ArtifactPythonFile:     {dstPath: filepath.Join(s.runDir, "main.py")},
		db.ArtifactEnvFile:        {dstPath: filepath.Join(envDir, "env")},

		db.ArtifactTSService: {dstPath: s.tailscaledServicePath(), unit: s.tailscaledServiceUnit()},
		db.ArtifactTSEnv:     {dstPath: filepath.Join(envDir, "tailscaled.env")},
		db.ArtifactTSBinary:  {dstPath: filepath.Join(binDir, "tailscaled")},
		db.ArtifactTSConfig:  {dstPath: filepath.Join(envDir, "tailscaled.json")},
	}
	// Self-managed host services retain their flat compatibility layout. Catch
	// is also the stable runner embedded in VM units. Native workload units
	// execute their immutable generation directly and have no such copy.
	if s.keepsStableRuntimeBinary() {
		installers[db.ArtifactBinary] = artifactInstall{dstPath: filepath.Join(s.runDir, s.Name())}
	}
	return installers
}

func (s *SystemdService) keepsStableRuntimeBinary() bool {
	return s.Name() == catchSystemServiceName || s.flatRuntimeArtifacts
}

func (s *SystemdService) installPlan() []installStep {
	installPaths := s.artifactInstaller()
	artifactOrder := []db.ArtifactName{
		db.ArtifactSystemdUnit,
		db.ArtifactSystemdTimerFile,
		db.ArtifactNetNSService,
		db.ArtifactNetNSEnv,
	}
	if s.keepsStableRuntimeBinary() {
		artifactOrder = append(artifactOrder, db.ArtifactBinary)
	}
	artifactOrder = append(artifactOrder,
		db.ArtifactTypeScriptFile,
		db.ArtifactPythonFile,
		db.ArtifactEnvFile,
		db.ArtifactTSService,
		db.ArtifactTSEnv,
		db.ArtifactTSBinary,
		db.ArtifactTSConfig,
	)
	plan := make([]installStep, 0, len(artifactOrder))
	for _, artifact := range artifactOrder {
		plan = append(plan, installStep{
			artifact:        artifact,
			artifactInstall: installPaths[artifact],
		})
	}
	return plan
}

func enabledUnitsForInstallPlan(plan []installStep, af db.ArtifactStore, gen int) []string {
	units := []string{}
	for _, step := range plan {
		if _, ok := af.Gen(step.artifact, gen); !ok || step.unit == "" {
			continue
		}
		log.Printf("adding unit %s to enable list", step.unit)
		if step.primaryUnitIfAvailable && len(units) > 0 {
			units[0] = step.unit
			continue
		}
		units = append(units, step.unit)
	}
	return units
}

func (s *SystemdService) installArtifacts(plan []installStep) error {
	af := s.cfg.AsStruct().Artifacts
	for _, step := range plan {
		srcPath, ok := af.Gen(step.artifact, s.cfg.Generation())
		if !ok {
			log.Printf("no %s artifact to install", step.artifact)
			if err := removeOptionalArtifact(step.dstPath); err != nil {
				return err
			}
			continue
		}
		log.Printf("copying %s to %s", srcPath, step.dstPath)
		if err := s.installArtifact(step, srcPath); err != nil {
			return err
		}
	}
	return nil
}

func (s *SystemdService) installArtifact(step installStep, srcPath string) error {
	if !isSystemdUnitArtifact(step.artifact) {
		if err := fileutil.CopyFile(srcPath, step.dstPath); err != nil {
			return err
		}
		return s.enforceManagedArtifactMetadata(step)
	}
	raw, mode, err := s.renderSystemdUnitArtifact(step, srcPath)
	if err != nil {
		return err
	}
	return writeInstalledSystemdUnit(step.dstPath, raw, mode)
}

func (s *SystemdService) enforceManagedArtifactMetadata(step installStep) (retErr error) {
	file, err := os.OpenFile(step.dstPath, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("managed artifact destination %s is not a regular file", step.dstPath)
	}
	uid, gid, mode, managed := s.managedArtifactMetadata(step.artifact, info.Mode())
	if !managed {
		return nil
	}
	if err := systemdInstallArtifactChown(file, int(uid), int(gid)); err != nil {
		return fmt.Errorf("set managed artifact owner %s to %d:%d: %w", step.dstPath, uid, gid, err)
	}
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set managed artifact mode %s to %04o: %w", step.dstPath, mode, err)
	}
	return file.Sync()
}

func (s *SystemdService) managedArtifactMetadata(artifact db.ArtifactName, current os.FileMode) (uint32, uint32, os.FileMode, bool) {
	identity := s.cfg.Identity()
	if !identity.Valid() {
		return 0, 0, 0, false
	}
	switch artifact {
	case db.ArtifactEnvFile, db.ArtifactNetNSEnv, db.ArtifactTSEnv, db.ArtifactTSConfig:
		return uint32(os.Geteuid()), identity.GID(), tightenedManagedArtifactMode(current, 0o640, 0o040), true
	case db.ArtifactBinary, db.ArtifactTSBinary:
		return uint32(os.Geteuid()), identity.GID(), tightenedManagedArtifactMode(current, 0o750, 0o050), true
	case db.ArtifactTypeScriptFile, db.ArtifactPythonFile:
		return identity.UID(), identity.GID(), current.Perm(), true
	default:
		return 0, 0, 0, false
	}
}

func tightenedManagedArtifactMode(current, allowed, required os.FileMode) os.FileMode {
	mode := current.Perm() & allowed.Perm()
	if mode&required.Perm() != required.Perm() {
		mode |= required.Perm()
	}
	return mode
}

func isSystemdUnitArtifact(artifact db.ArtifactName) bool {
	switch artifact {
	case db.ArtifactSystemdUnit, db.ArtifactSystemdTimerFile, db.ArtifactNetNSService, db.ArtifactTSService:
		return true
	default:
		return false
	}
}

func (s *SystemdService) renderSystemdUnitArtifact(step installStep, srcPath string) (_ []byte, _ os.FileMode, retErr error) {
	src, err := os.OpenFile(srcPath, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if closeErr := src.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	info, err := validateSystemdInstallTargetArtifact(src, step.artifact, srcPath)
	if err != nil {
		return nil, 0, err
	}
	raw, err := io.ReadAll(src)
	if err != nil {
		return nil, 0, err
	}
	raw, err = s.renderSystemdUnitContent(step, raw)
	if err != nil {
		return nil, 0, fmt.Errorf("render systemd unit artifact %s: %w", srcPath, err)
	}
	return raw, info.Mode().Perm(), nil
}

func (s *SystemdService) renderSystemdUnitContent(step installStep, raw []byte) ([]byte, error) {
	raw = []byte(s.rewriteLegacyRuntimePaths(string(raw)))
	identity := s.cfg.Identity()
	if step.artifact == db.ArtifactSystemdUnit && identity.Valid() {
		rewritten, err := rewriteInstalledSystemdUnitIdentity(string(raw), identity.RequestedUser(), identity.RequestedGroup())
		if err != nil {
			return nil, err
		}
		raw = []byte(rewritten)
	}
	return raw, nil
}

func (s *SystemdService) rewriteLegacyRuntimePaths(raw string) string {
	if s.keepsStableRuntimeBinary() {
		return raw
	}
	service := s.cfg.AsStruct()
	installers := s.artifactInstaller()
	type replacement struct{ old, new string }
	var replacements []replacement
	add := func(artifact db.ArtifactName, oldName string, immutable bool) {
		path, ok := service.Artifacts.Gen(artifact, service.Generation)
		if !ok {
			return
		}
		if !immutable {
			path = installers[artifact].dstPath
		}
		replacements = append(replacements, replacement{old: filepath.Join(s.runDir, oldName), new: path})
	}
	add(db.ArtifactTSEnv, "tailscaled.env", false)
	add(db.ArtifactTSConfig, "tailscaled.json", false)
	add(db.ArtifactTSBinary, "tailscaled", false)
	add(db.ArtifactNetNSEnv, "netns.env", false)
	add(db.ArtifactEnvFile, "env", false)
	add(db.ArtifactBinary, s.Name(), true)
	sort.Slice(replacements, func(i, j int) bool { return len(replacements[i].old) > len(replacements[j].old) })
	for _, replacement := range replacements {
		raw = strings.ReplaceAll(raw, replacement.old, replacement.new)
	}
	return raw
}

// RenderedPrimaryUnit returns the exact primary unit content that installation
// would publish for the current generation.
func (s *SystemdService) RenderedPrimaryUnit() (string, error) {
	step := installStep{artifact: db.ArtifactSystemdUnit, artifactInstall: s.artifactInstaller()[db.ArtifactSystemdUnit]}
	source, ok := s.cfg.AsStruct().Artifacts.Gen(step.artifact, s.cfg.Generation())
	if !ok {
		return "", fmt.Errorf("service %q generation %d has no systemd unit artifact", s.Name(), s.cfg.Generation())
	}
	raw, _, err := s.renderSystemdUnitArtifact(step, source)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// RenderPrimaryUnit returns the exact primary unit content that installation
// would publish for raw generated unit content.
func (s *SystemdService) RenderPrimaryUnit(raw string) (string, error) {
	step := installStep{artifact: db.ArtifactSystemdUnit, artifactInstall: s.artifactInstaller()[db.ArtifactSystemdUnit]}
	rendered, err := s.renderSystemdUnitContent(step, []byte(raw))
	if err != nil {
		return "", err
	}
	return string(rendered), nil
}

func rewriteInstalledSystemdUnitIdentity(raw, user, group string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("unit is empty")
	}
	rewriter := installedSystemdIdentityRewriter{
		user: user, updates: map[string]string{"User": user, "Group": group}, seen: map[string]bool{},
	}
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	rewriter.out = make([]string, 0, len(lines)+3)
	for _, line := range lines {
		rewriter.appendLine(line)
	}
	if !rewriter.seenService {
		return "", fmt.Errorf("unit has no [Service] section")
	}
	if rewriter.inService {
		rewriter.flush()
	}
	return strings.Join(rewriter.out, "\n") + "\n", nil
}

type installedSystemdIdentityRewriter struct {
	user             string
	updates          map[string]string
	seen             map[string]bool
	out              []string
	inService        bool
	seenService      bool
	workingDirectory string
}

func (r *installedSystemdIdentityRewriter) appendLine(line string) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		r.appendSection(line, trimmed)
		return
	}
	if r.inService && (r.appendUpdatedDirective(trimmed) || isSystemdIdentityEnvironment(trimmed)) {
		return
	}
	r.out = append(r.out, line)
}

func (r *installedSystemdIdentityRewriter) appendSection(line, section string) {
	if r.inService {
		r.flush()
	}
	r.inService = section == "[Service]"
	r.seenService = r.seenService || r.inService
	r.out = append(r.out, line)
}

func (r *installedSystemdIdentityRewriter) appendUpdatedDirective(line string) bool {
	key, _, ok := strings.Cut(line, "=")
	if !ok {
		return false
	}
	if key == "WorkingDirectory" {
		r.workingDirectory = strings.TrimSpace(strings.TrimPrefix(line, "WorkingDirectory="))
	}
	value, replace := r.updates[key]
	if !replace {
		return false
	}
	if !r.seen[key] {
		r.out = append(r.out, key+"="+value)
		r.seen[key] = true
	}
	return true
}

func (r *installedSystemdIdentityRewriter) flush() {
	for _, key := range []string{"User", "Group"} {
		if !r.seen[key] {
			r.out = append(r.out, key+"="+r.updates[key])
			r.seen[key] = true
		}
	}
	if r.workingDirectory != "" {
		r.out = append(r.out, systemdIdentityEnvironment(r.user, r.workingDirectory))
	}
}

func systemdIdentityEnvironment(user, workingDirectory string) string {
	return "Environment=HOME=" + workingDirectory + " USER=" + user + " LOGNAME=" + user + " SHELL=/bin/sh"
}

func isSystemdIdentityEnvironment(line string) bool {
	return strings.HasPrefix(line, "Environment=HOME=") && strings.Contains(line, " USER=") &&
		strings.Contains(line, " LOGNAME=") && strings.HasSuffix(line, " SHELL=/bin/sh")
}

func writeInstalledSystemdUnit(path string, raw []byte, mode os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := tmp.Close(); retErr == nil {
				retErr = closeErr
			}
		}
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return fileutil.SyncDir(dir)
}

func removeOptionalArtifact(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove optional artifact %s: %v", path, err)
	} else if err == nil {
		if syncErr := fileutil.SyncDir(filepath.Dir(path)); syncErr != nil {
			return fmt.Errorf("sync optional artifact removal %s: %w", path, syncErr)
		}
		log.Printf("removed optional artifact %s", path)
	}
	return nil
}

func (s *SystemdService) Install() error {
	return s.InstallWithActivationCheck(nil)
}

type installDestinationRollback struct {
	path    string
	present bool
	raw     []byte
	mode    os.FileMode
}

func captureInstallDestinationRollback(plan []installStep) ([]installDestinationRollback, error) {
	states := make([]installDestinationRollback, 0, len(plan))
	for _, step := range plan {
		state := installDestinationRollback{path: step.dstPath}
		file, err := os.OpenFile(step.dstPath, os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if errors.Is(err, os.ErrNotExist) {
			states = append(states, state)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("capture install destination %s: %w", step.dstPath, err)
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("inspect install destination %s: %w", step.dstPath, statErr)
		}
		if !info.Mode().IsRegular() {
			_ = file.Close()
			return nil, fmt.Errorf("install destination %s is not a regular file", step.dstPath)
		}
		raw, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, fmt.Errorf("read install destination %s: %w", step.dstPath, errors.Join(readErr, closeErr))
		}
		state.present = true
		state.raw = raw
		state.mode = info.Mode().Perm()
		states = append(states, state)
	}
	return states, nil
}

func restoreInstallDestinations(states []installDestinationRollback) error {
	var restoreErr error
	for index := len(states) - 1; index >= 0; index-- {
		state := states[index]
		var err error
		if state.present {
			err = writeInstalledSystemdUnit(state.path, state.raw, state.mode)
		} else {
			err = removeOptionalArtifact(state.path)
		}
		if err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore install destination %s: %w", state.path, err))
		}
	}
	return restoreErr
}

// InstallWithActivationCheck stages all stable destinations, then runs check
// before daemon-reload or enable can activate them.
func (s *SystemdService) InstallWithActivationCheck(check func() error) error {
	var rollback []installDestinationRollback
	if check != nil {
		var err error
		rollback, err = captureInstallDestinationRollback(s.installPlan())
		if err != nil {
			return err
		}
	}
	units, err := s.StageInstallForReload()
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(); err != nil {
			return errors.Join(err, restoreInstallDestinations(rollback))
		}
	}

	if err := s.run("daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd: %v", err)
	}

	for _, unit := range units {
		if err := s.run("enable", unit); err != nil {
			return fmt.Errorf("failed to enable %s: %v", unit, err)
		}
	}
	return nil
}

func (s *SystemdService) StageInstallForReload() ([]string, error) {
	return s.StageInstallForReloadExcluding()
}

// StageInstallForReloadExcluding stages a generation while leaving the named
// stable destinations untouched. Transactions use this to reserve the primary
// unit for their separately journaled atomic unit-write phase.
func (s *SystemdService) StageInstallForReloadExcluding(excluded ...string) ([]string, error) {
	plan := s.installPlan()
	excludedPaths := make(map[string]struct{}, len(excluded))
	for _, path := range excluded {
		excludedPaths[filepath.Clean(path)] = struct{}{}
	}
	filtered := make([]installStep, 0, len(plan))
	for _, step := range plan {
		if _, skip := excludedPaths[filepath.Clean(step.dstPath)]; !skip {
			filtered = append(filtered, step)
		}
	}
	if err := s.installArtifacts(filtered); err != nil {
		return nil, err
	}
	return enabledUnitsForInstallPlan(plan, s.cfg.AsStruct().Artifacts, s.cfg.Generation()), nil
}

// InstallTargetPaths returns every stable path that StageInstallForReload may
// replace or remove. Callers that wrap definition installation in a larger
// transaction use this to preserve the prior files before staging.
func (s *SystemdService) InstallTargetPaths() []string {
	plan := s.installPlan()
	paths := make([]string, 0, len(plan))
	for _, step := range plan {
		paths = append(paths, step.dstPath)
	}
	return paths
}

// InstallTargetStatesExcluding computes the exact semantic destination states
// before a transactional install mutates any stable path.
func (s *SystemdService) InstallTargetStatesExcluding(excluded ...string) ([]InstallTargetState, error) {
	excludedPaths := make(map[string]struct{}, len(excluded))
	for _, path := range excluded {
		excludedPaths[filepath.Clean(path)] = struct{}{}
	}
	af := s.cfg.AsStruct().Artifacts
	states := make([]InstallTargetState, 0, len(s.installPlan()))
	for _, step := range s.installPlan() {
		path := filepath.Clean(step.dstPath)
		if _, skip := excludedPaths[path]; skip {
			continue
		}
		state, err := s.installTargetState(step, af)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

func (s *SystemdService) installTargetState(step installStep, artifacts db.ArtifactStore) (InstallTargetState, error) {
	state := InstallTargetState{Path: filepath.Clean(step.dstPath)}
	source, present := artifacts.Gen(step.artifact, s.cfg.Generation())
	if !present {
		return state, nil
	}
	if isSystemdUnitArtifact(step.artifact) {
		raw, mode, err := s.renderSystemdUnitArtifact(step, source)
		if err != nil {
			return InstallTargetState{}, err
		}
		hash := sha256.Sum256(raw)
		state.Present = true
		state.Mode = mode
		state.UID = uint32(os.Geteuid())
		state.GID = uint32(os.Getegid())
		state.Nlink = 1
		state.Size = int64(len(raw))
		state.SHA256 = hex.EncodeToString(hash[:])
		return state, nil
	}
	file, err := os.OpenFile(source, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return InstallTargetState{}, fmt.Errorf("open %s artifact for transaction intent: %w", step.artifact, err)
	}
	info, err := validateSystemdInstallTargetArtifact(file, step.artifact, source)
	if err != nil {
		_ = file.Close()
		return InstallTargetState{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return InstallTargetState{}, err
	}
	if err := file.Close(); err != nil {
		return InstallTargetState{}, err
	}
	state.Present = true
	state.Mode = info.Mode()
	state.UID = uint32(os.Geteuid())
	state.GID = uint32(os.Getegid())
	if uid, gid, mode, managed := s.managedArtifactMetadata(step.artifact, info.Mode()); managed {
		state.Mode = mode
		state.UID = uid
		state.GID = gid
	}
	state.Nlink = 1
	state.Size = info.Size()
	state.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return state, nil
}

func validateSystemdInstallTargetArtifact(file *os.File, artifact db.ArtifactName, source string) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || stat.Nlink != 1 {
		return nil, fmt.Errorf("%s artifact %s is not a safe single-link regular file", artifact, source)
	}
	unsupportedXattrs, err := systemdInstallTargetHasUnsupportedXattrs(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	if unsupportedXattrs {
		return nil, fmt.Errorf("%s artifact %s has extended attributes that transactional staging cannot preserve", artifact, source)
	}
	return info, nil
}

func systemdInstallTargetHasUnsupportedXattrs(fd int) (bool, error) {
	size, err := systemdInstallTargetFlistxattr(fd, nil)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENODATA) {
		return false, nil
	}
	if err != nil || size == 0 {
		return false, err
	}
	buf := make([]byte, size)
	n, err := systemdInstallTargetFlistxattr(fd, buf)
	if err != nil {
		return false, err
	}
	return systemdInstallTargetXattrsUnsupportedForOS(runtime.GOOS, buf[:n]), nil
}

func systemdInstallTargetXattrsUnsupportedForOS(goos string, raw []byte) bool {
	for _, name := range strings.Split(string(raw), "\x00") {
		if name != "" && (goos != "darwin" || name != "com.apple.provenance") {
			return true
		}
	}
	return false
}

// PrimaryUnitPath returns the stable systemd unit destination for this service.
func (s *SystemdService) PrimaryUnitPath() string {
	return s.servicePath()
}

// InstallUnits returns the units that Install enables for the current
// generation without copying artifacts or invoking systemctl.
func (s *SystemdService) InstallUnits() []string {
	plan := s.installPlan()
	return enabledUnitsForInstallPlan(plan, s.cfg.AsStruct().Artifacts, s.cfg.Generation())
}

func (s *SystemdService) serviceUnit() string {
	return s.Name() + ".service"
}

func (s *SystemdService) timerUnit() string {
	return s.Name() + ".timer"
}

func (s *SystemdService) netnsServiceUnit() string {
	return "yeet-" + s.Name() + "-ns.service"
}

func (s *SystemdService) tailscaledServiceUnit() string {
	return "yeet-" + s.Name() + "-ts.service"
}

func (s *SystemdService) servicePath() string {
	return filepath.Join(s.systemdSystemDir(), s.serviceUnit())
}

func (s *SystemdService) tailscaledServicePath() string {
	return filepath.Join(s.systemdSystemDir(), s.tailscaledServiceUnit())
}

func (s *SystemdService) timerPath() string {
	return filepath.Join(s.systemdSystemDir(), s.timerUnit())
}

func (s *SystemdService) netnsServicePath() string {
	return filepath.Join(s.systemdSystemDir(), s.netnsServiceUnit())
}

func (s *SystemdService) systemdSystemDir() string {
	if s.systemdDir != "" {
		return s.systemdDir
	}
	return "/etc/systemd/system"
}

func (s *SystemdService) isInstalled() bool {
	if s.isTimer() && !fileExists(s.timerPath()) {
		return false
	}
	return fileExists(s.servicePath())
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (s *SystemdService) isTimer() bool {
	return s.hasArtifact(db.ArtifactSystemdTimerFile)
}

func (s *SystemdService) PrimaryUnit() string {
	return s.primaryUnit()
}

func (s *SystemdService) primaryUnit() string {
	if s.isTimer() {
		return s.timerUnit()
	}
	return s.serviceUnit()
}

func (s *SystemdService) Uninstall() error {
	for _, target := range s.uninstallUnitTargets() {
		if err := s.disableAndRemoveUnitIfPresent(target.unit, target.path); err != nil {
			return err
		}
	}
	return s.run("daemon-reload")
}

type systemdUnitTarget struct {
	unit string
	path string
}

func (s *SystemdService) uninstallUnitTargets() []systemdUnitTarget {
	return []systemdUnitTarget{
		{unit: s.timerUnit(), path: s.timerPath()},
		{unit: s.serviceUnit(), path: s.servicePath()},
		{unit: s.netnsServiceUnit(), path: s.netnsServicePath()},
		{unit: s.tailscaledServiceUnit(), path: s.tailscaledServicePath()},
	}
}

func (s *SystemdService) disableAndRemoveUnitIfPresent(unit, path string) error {
	present, err := systemdUnitPathPresent(path)
	if err != nil {
		return fmt.Errorf("inspect systemd unit %s: %w", unit, err)
	}
	if !present {
		return nil
	}
	if err := s.run("disable", "--now", unit); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func systemdUnitPathPresent(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (s *SystemdService) uninstallDisableUnits() []string {
	return []string{s.primaryUnit(), s.netnsServiceUnit(), s.tailscaledServiceUnit()}
}

func (s *SystemdService) Status() (Status, error) {
	if !s.isInstalled() {
		return StatusUnknown, nil
	}
	if err := s.run("is-active", s.primaryUnit()); err != nil {
		return StatusStopped, nil
	}
	return StatusRunning, nil
}

func (s *SystemdService) isActive(unit string) bool {
	if err := s.run("is-active", unit); err != nil {
		return false
	}
	return true
}

func (s *SystemdService) monitorTailscale() (err error) {
	log.Printf("monitoring tailscale for %s", s.Name())
	sock := filepath.Join(s.runDir, "tailscaled.sock")
	lc := local.Client{
		Socket:        sock,
		UseSocketOnly: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	bo := backoff.NewBackoff("tailscale monitor", log.Printf, time.Minute)
	return runTailscaleMonitorLoop(ctx, func() error {
		return s.monitorTailscaleBus(ctx, &lc, bo)
	}, func(err error) {
		log.Printf("tailscaled socket not found, retrying")
		bo.BackOff(ctx, err)
	})
}

func runTailscaleMonitorLoop(ctx context.Context, watch func() error, retryMissingSocket func(error)) error {
	for {
		err := watch()
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		retryMissingSocket(err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (s *SystemdService) monitorTailscaleBus(ctx context.Context, lc *local.Client, bo *backoff.Backoff) (err error) {
	bus, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialNetMap)
	if err != nil {
		return tailscaleWatchError(ctx, err)
	}
	defer closeIPNBus(bus, &err)
	bo.BackOff(ctx, nil)
	return s.storeTailscaleStableID(bus)
}

func tailscaleWatchError(ctx context.Context, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func closeIPNBus(bus *local.IPNBusWatcher, err *error) {
	if closeErr := bus.Close(); closeErr != nil && *err == nil {
		*err = closeErr
	}
}

func (s *SystemdService) storeTailscaleStableID(bus *local.IPNBusWatcher) error {
	for {
		msg, err := bus.Next()
		if err != nil {
			return err
		}
		if msg.NetMap == nil {
			continue
		}
		_, _, err = s.db.MutateService(s.cfg.Name(), func(d *db.Data, s *db.Service) error {
			s.TSNet.StableID = msg.NetMap.SelfNode.StableID()
			return nil
		})
		return err
	}
}

func (s *SystemdService) Start() error {
	if err := s.StartAuxiliaryUnits(); err != nil {
		return err
	}
	return s.run("start", s.primaryUnit())
}

func (s *SystemdService) StartAuxiliaryUnits() error {
	af := s.cfg.AsStruct().Artifacts
	if _, ok := af.Gen(db.ArtifactNetNSService, s.cfg.Generation()); ok {
		if err := s.run("start", s.netnsServiceUnit()); err != nil {
			return err
		}
	}
	if _, ok := af.Gen(db.ArtifactTSService, s.cfg.Generation()); ok {
		ctx, cancel := context.WithTimeout(context.Background(), tailscaleReadyTimeout)
		defer cancel()
		if err := s.StartTailscaleSidecar(ctx); err != nil {
			return err
		}
		go func() {
			if err := monitorTailscaleFn(s); err != nil {
				log.Printf("failed to monitor tailscale: %v", err)
			}
		}()
	}
	return nil
}

func (s *SystemdService) hasArtifact(a db.ArtifactName) bool {
	af, ok := s.cfg.Artifacts().GetOk(a)
	if !ok {
		return false
	}
	_, ok = af.Refs().GetOk(db.Gen(s.cfg.Generation()))
	return ok
}

func (s *SystemdService) Stop() error {
	if err := s.stopPrimaryIfInstalled(); err != nil {
		return err
	}
	s.stopAuxiliaryUnitsForCleanup()
	return nil
}

func (s *SystemdService) stopPrimaryIfInstalled() error {
	if s.isInstalled() {
		for _, unit := range s.primaryStopUnits() {
			if err := s.run("stop", unit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SystemdService) stopAuxiliaryUnitsForCleanup() {
	for _, unit := range s.auxiliaryStopUnits() {
		if err := s.run("stop", unit); err != nil {
			log.Printf("failed to stop optional unit %s during cleanup: %v", unit, err)
		}
	}
}

func (s *SystemdService) stopUnits() []string {
	units := s.primaryStopUnits()
	return append(units, s.auxiliaryStopUnits()...)
}

func (s *SystemdService) primaryStopUnits() []string {
	units := []string{s.primaryUnit()}
	if s.isTimer() {
		// Also stop the service if it's a timer.
		units = append(units, s.serviceUnit())
	}
	return units
}

func (s *SystemdService) auxiliaryStopUnits() []string {
	units := []string{}
	if s.hasArtifact(db.ArtifactTSService) {
		units = append(units, s.tailscaledServiceUnit())
	}
	if s.hasArtifact(db.ArtifactNetNSService) {
		units = append(units, s.netnsServiceUnit())
	}
	return units
}

func (s *SystemdService) Restart() error {
	if s.isActive(s.primaryUnit()) {
		if s.isTimer() {
			if err := s.run("stop", s.serviceUnit()); err != nil {
				return err
			}
		}
		return s.run("restart", s.primaryUnit())
	}
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start()
}

func (s *SystemdService) Enable() error {
	return s.run("enable", s.primaryUnit())
}

func (s *SystemdService) Disable() error {
	if !s.isInstalled() {
		return nil
	}
	return s.run("disable", s.primaryUnit())
}
