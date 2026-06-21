// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Masterminds/semver/v3"
	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/util/mak"
)

var (
	checkMountCommand = func(mountType string) error {
		mountCmd := fmt.Sprintf("/sbin/mount.%s", mountType)
		_, err := os.Stat(mountCmd)
		if err != nil {
			return fmt.Errorf("mount command %q not found", mountCmd)
		}
		return nil
	}
	mountVolume = func(vol db.Volume) error {
		return (&systemdMounter{v: vol}).mount()
	}
	unmountVolume = func(e *ttyExecer, vol db.Volume) error {
		return (&systemdMounter{e: e, v: vol}).umount()
	}
	dockerComposeUpdate = (*svc.DockerComposeService).Update
)

var snapshotCommandHandlers = map[string]func(*ttyExecer, []string) error{
	"defaults":  (*ttyExecer).snapshotsDefaultsCmdFunc,
	"create":    (*ttyExecer).snapshotsCreateCmdFunc,
	"clone":     (*ttyExecer).snapshotsCloneCmdFunc,
	"restore":   (*ttyExecer).snapshotsRestoreCmdFunc,
	"list":      (*ttyExecer).snapshotsListCmdFunc,
	"inspect":   (*ttyExecer).snapshotsInspectCmdFunc,
	"rm":        (*ttyExecer).snapshotsRemoveCmdFunc,
	"protect":   (*ttyExecer).snapshotsProtectCmdFunc,
	"unprotect": (*ttyExecer).snapshotsUnprotectCmdFunc,
}

func (e *ttyExecer) dockerCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("docker requires a subcommand")
	}
	subcmd := args[0]
	args = args[1:]
	switch subcmd {
	case "pull":
		if len(args) > 0 {
			return fmt.Errorf("docker pull takes no arguments")
		}
		return e.dockerPullCmdFunc()
	case "update":
		if len(args) > 0 {
			return fmt.Errorf("docker update takes no arguments")
		}
		return e.dockerUpdateCmdFunc()
	case "outdated":
		flags, remaining, err := cli.ParseDockerOutdated(args)
		if err != nil {
			return err
		}
		if len(remaining) > 0 {
			return fmt.Errorf("docker outdated takes no remote arguments")
		}
		return e.dockerOutdatedCmdFunc(flags)
	default:
		return fmt.Errorf("unknown docker command %q", subcmd)
	}
}

func (e *ttyExecer) dockerOutdatedCmdFunc(flags cli.DockerOutdatedFlags) error {
	if err := validateDockerOutdatedFormat(flags.Format); err != nil {
		return err
	}
	var rows []svc.DockerOutdatedRow
	var err error
	if e.sn == SystemService {
		if e.dockerOutdatedAllFunc != nil {
			rows, err = e.dockerOutdatedAllFunc(e.ctx)
		} else {
			rows, err = e.s.DockerComposeOutdatedAll(e.ctx)
		}
	} else {
		st, typeErr := e.s.serviceType(e.sn)
		if typeErr != nil {
			return fmt.Errorf("failed to get service type: %w", typeErr)
		}
		if st != db.ServiceTypeDockerCompose {
			return fmt.Errorf("service %q is not a docker compose service", e.sn)
		}
		if e.dockerOutdatedFunc != nil {
			rows, err = e.dockerOutdatedFunc(e.ctx, e.sn, svc.DockerOutdatedOptions{IncludeInternal: true})
		} else {
			rows, err = e.s.DockerComposeOutdated(e.ctx, e.sn, svc.DockerOutdatedOptions{IncludeInternal: true})
		}
	}
	if err != nil {
		return err
	}
	sortDockerOutdatedRows(rows)
	return renderDockerOutdatedRows(e.rw, flags.Format, rows)
}

func renderDockerOutdatedRows(w io.Writer, formatOut string, rows []svc.DockerOutdatedRow) error {
	formatOut = strings.TrimSpace(formatOut)
	if err := validateDockerOutdatedFormat(formatOut); err != nil {
		return err
	}
	switch formatOut {
	case "json":
		return json.NewEncoder(w).Encode(rows)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	case "", "table":
		return renderDockerOutdatedTable(w, rows)
	}
	return nil
}

func validateDockerOutdatedFormat(formatOut string) error {
	switch strings.TrimSpace(formatOut) {
	case "", "table", "json", "json-pretty":
		return nil
	default:
		return fmt.Errorf("unsupported docker outdated format %q", formatOut)
	}
}

func renderDockerOutdatedTable(w io.Writer, rows []svc.DockerOutdatedRow) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SERVICE\tCONTAINER\tIMAGE\tUPDATE"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			row.ServiceName,
			dash(row.ContainerName),
			svc.CompactDockerOutdatedImageRef(row.Image),
			svc.CompactDockerOutdatedStatus(row.Status, row.Reason),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func sortDockerOutdatedRows(rows []svc.DockerOutdatedRow) {
	slices.SortFunc(rows, func(a, b svc.DockerOutdatedRow) int {
		if a.ServiceName != b.ServiceName {
			return strings.Compare(a.ServiceName, b.ServiceName)
		}
		if a.ContainerName != b.ContainerName {
			return strings.Compare(a.ContainerName, b.ContainerName)
		}
		return strings.Compare(a.Image, b.Image)
	})
}

func (e *ttyExecer) dockerComposeServiceCmd() (*svc.DockerComposeService, error) {
	st, err := e.s.serviceType(e.sn)
	if err != nil {
		return nil, fmt.Errorf("failed to get service type: %w", err)
	}
	if st != db.ServiceTypeDockerCompose {
		return nil, fmt.Errorf("service %q is not a docker compose service", e.sn)
	}
	docker, err := e.s.dockerComposeService(e.sn)
	if err != nil {
		return nil, err
	}
	docker.NewCmd = e.newCmd
	docker.NewCmdContext = e.newCmdContext
	return docker, nil
}

func (e *ttyExecer) dockerPullCmdFunc() error {
	docker, err := e.dockerComposeServiceCmd()
	if err != nil {
		return err
	}
	return docker.Pull()
}

func (e *ttyExecer) dockerUpdateCmdFunc() error {
	ui := e.newProgressUI("docker update")
	ui.Start()
	defer ui.Stop()
	ui.StartStep("Update service")
	// Stop the spinner so compose output has a clean line to write to.
	ui.Suspend()
	docker, err := e.dockerComposeServiceCmd()
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	if err := e.s.withServiceSnapshot(e.ctx, snapshotOperation{
		Service:   sv.AsStruct(),
		Event:     snapshotEventDockerUpdate,
		Writer:    e.rw,
		Operation: func() error { return dockerComposeUpdate(docker) },
	}); err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep("")
	return nil
}

func (e *ttyExecer) snapshotsCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("snapshots requires a subcommand")
	}
	handler, ok := snapshotCommandHandlers[args[0]]
	if !ok {
		return fmt.Errorf("unknown snapshots command %q", args[0])
	}
	handlerArgs := args[1:]
	if args[0] == "defaults" {
		handlerArgs = args
	}
	return handler(e, handlerArgs)
}

func (e *ttyExecer) snapshotsCreateCmdFunc(args []string) error {
	flags, rest, err := cli.ParseSnapshotsCreate(args)
	if err != nil {
		return err
	}
	return e.s.createRecoveryPoint(e.vmProvisionContext(), rest[0], flags, e.rw)
}

func (e *ttyExecer) snapshotsCloneCmdFunc(args []string) error {
	flags, rest, err := cli.ParseSnapshotsClone(args)
	if err != nil {
		return err
	}
	return e.s.cloneRecoveryPoint(e.ctx, rest[0], rest[1], rest[2], flags, e.rw)
}

func (e *ttyExecer) snapshotsRestoreCmdFunc(args []string) error {
	flags, rest, err := cli.ParseSnapshotsRestore(args)
	if err != nil {
		return err
	}
	return e.s.restoreRecoveryPoint(e.ctx, rest[0], rest[1], flags, e.rw)
}

func (e *ttyExecer) snapshotsListCmdFunc(args []string) error {
	flags, rest, err := cli.ParseSnapshotsList(args)
	if err != nil {
		return err
	}
	service := ""
	if len(rest) == 1 {
		service = rest[0]
	}
	points, err := e.s.listRecoveryPoints(e.ctx, service)
	if err != nil {
		return err
	}
	return renderRecoveryPoints(e.rw, flags.Format, points)
}

func (e *ttyExecer) snapshotsInspectCmdFunc(args []string) error {
	flags, rest, err := cli.ParseSnapshotsInspect(args)
	if err != nil {
		return err
	}
	points, err := e.s.listRecoveryPoints(e.ctx, rest[0])
	if err != nil {
		return err
	}
	point, err := resolveRecoveryPointSelector(points, rest[1])
	if err != nil {
		return err
	}
	return renderRecoveryPointInspect(e.rw, flags.Format, point)
}

func (e *ttyExecer) snapshotsRemoveCmdFunc(args []string) error {
	flags, rest, err := cli.ParseSnapshotsRemove(args)
	if err != nil {
		return err
	}
	return e.s.removeRecoveryPoint(e.ctx, rest[0], rest[1], flags.Yes, e.rw)
}

func (e *ttyExecer) snapshotsProtectCmdFunc(args []string) error {
	rest, err := cli.ParseSnapshotsProtect(args, "protect")
	if err != nil {
		return err
	}
	return e.s.setRecoveryPointProtected(e.ctx, rest[0], rest[1], true, e.rw)
}

func (e *ttyExecer) snapshotsUnprotectCmdFunc(args []string) error {
	rest, err := cli.ParseSnapshotsProtect(args, "unprotect")
	if err != nil {
		return err
	}
	return e.s.setRecoveryPointProtected(e.ctx, rest[0], rest[1], false, e.rw)
}

func (e *ttyExecer) snapshotsDefaultsCmdFunc(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("snapshots requires defaults show or defaults set")
	}
	switch args[1] {
	case "show":
		if _, err := cli.ParseSnapshotDefaultsShow(args[2:]); err != nil {
			return err
		}
		return e.showSnapshotDefaults()
	case "set":
		flags, rest, err := cli.ParseSnapshotDefaultsSet(args[2:])
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return fmt.Errorf("unexpected snapshots defaults args: %s", strings.Join(rest, " "))
		}
		return e.setSnapshotDefaults(flags)
	default:
		return fmt.Errorf("unknown snapshots defaults command %q", args[1])
	}
}

func (e *ttyExecer) showSnapshotDefaults() error {
	dv, err := e.s.cfg.DB.Get()
	if err != nil {
		return err
	}
	defaults := snapshotPolicyPtrFromView(dv.SnapshotDefaults())
	effective, err := effectiveSnapshotPolicy(defaults, nil)
	if err != nil {
		return err
	}
	writef(e.rw, "# effective snapshot defaults\n")
	printSnapshotPolicy(e.rw, effectiveSnapshotPolicyRPCWithPreferred(effective, preferredEffectiveSnapshotMaxAge(defaults, nil)))
	return nil
}

func (e *ttyExecer) setSnapshotDefaults(flags cli.SnapshotDefaultsSetFlags) error {
	_, err := e.s.cfg.DB.MutateData(func(d *db.Data) error {
		if d.SnapshotDefaults == nil {
			d.SnapshotDefaults = &db.SnapshotPolicy{}
		}
		return applySnapshotDefaultsFlags(d.SnapshotDefaults, flags)
	})
	return err
}

func applySnapshotDefaultsFlags(policy *db.SnapshotPolicy, flags cli.SnapshotDefaultsSetFlags) error {
	if policy == nil {
		return fmt.Errorf("snapshot defaults policy is nil")
	}
	if err := applySnapshotBoolFlag(&policy.Enabled, "--enabled", flags.Enabled); err != nil {
		return err
	}
	if err := applySnapshotKeepLastFlag(policy, flags.KeepLast); err != nil {
		return err
	}
	if err := applySnapshotMaxAgeFlag(policy, flags.MaxAge); err != nil {
		return err
	}
	if err := applySnapshotBoolFlag(&policy.Required, "--required", flags.Required); err != nil {
		return err
	}
	if err := applySnapshotEventsFlag(policy, flags.Events); err != nil {
		return err
	}
	return nil
}

func applySnapshotBoolFlag(dst **bool, name, value string) error {
	if value == "" {
		return nil
	}
	v, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("invalid %s value %q", name, value)
	}
	*dst = &v
	return nil
}

func applySnapshotKeepLastFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return fmt.Errorf("--keep-last must be a positive integer")
	}
	policy.KeepLast = &n
	return nil
}

func applySnapshotMaxAgeFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	if _, err := parseSnapshotMaxAge(value); err != nil {
		return err
	}
	policy.MaxAge = value
	return nil
}

func applySnapshotEventsFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	events, err := parseSnapshotEvents(value)
	if err != nil {
		return err
	}
	policy.Events = events
	return nil
}

func printSnapshotPolicy(w io.Writer, policy catchrpc.EffectiveSnapshotPolicy) {
	writef(w, "enabled = %t\n", policy.Enabled)
	writef(w, "keep_last = %d\n", policy.KeepLast)
	writef(w, "max_age = %q\n", policy.MaxAge)
	writef(w, "events = [")
	for i, event := range policy.Events {
		if i > 0 {
			writef(w, ", ")
		}
		writef(w, "%q", event)
	}
	writef(w, "]\n")
	writef(w, "required = %t\n", policy.Required)
}

// Add this method to the ttyExecer struct
func (e *ttyExecer) eventsCmdFunc(flags cli.EventsFlags) error {
	ch := make(chan Event)
	all := flags.All
	defer e.s.RemoveEventListener(e.s.AddEventListener(ch, func(et Event) bool {
		if all {
			return true
		}
		return et.ServiceName == e.sn
	}))

	for {
		select {
		case event := <-ch:
			e.printf("Received event: %v\n", event)
		case <-e.ctx.Done():
			return nil
		}
	}
}

func (e *ttyExecer) umountCmdFunc(args []string) error {
	mountName, err := umountNameFromArgs(args)
	if err != nil {
		return err
	}
	d, vol, err := e.unmountVolumeData(mountName)
	if err != nil {
		return err
	}
	if err := unmountVolume(e, vol); err != nil {
		return fmt.Errorf("failed to umount %s: %w", vol.Path, err)
	}
	return e.deleteUnmountedVolume(d, mountName)
}

func umountNameFromArgs(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("invalid number of arguments")
	}
	return args[0], nil
}

func (e *ttyExecer) unmountVolumeData(mountName string) (*db.Data, db.Volume, error) {
	dv, err := e.s.cfg.DB.Get()
	if err != nil {
		return nil, db.Volume{}, fmt.Errorf("failed to get services: %w", err)
	}
	vol, ok := dv.Volumes().GetOk(mountName)
	if !ok {
		return nil, db.Volume{}, fmt.Errorf("volume %q not found", mountName)
	}
	return dv.AsStruct(), *vol.AsStruct(), nil
}

func (e *ttyExecer) deleteUnmountedVolume(d *db.Data, mountName string) error {
	delete(d.Volumes, mountName)
	if err := e.s.cfg.DB.Set(d); err != nil {
		return fmt.Errorf("failed to save data: %w", err)
	}
	return nil
}

func (e *ttyExecer) mountCmdFunc(flags cli.MountFlags, args []string) error {
	if len(args) == 0 {
		dv, err := e.s.cfg.DB.Get()
		if err != nil {
			return fmt.Errorf("failed to get services: %w", err)
		}
		return printMounts(e.rw, dv.AsStruct().Volumes)
	}
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("invalid number of arguments")
	}
	return e.mountCreateCmdFunc(flags, args)
}

func (e *ttyExecer) mountCreateCmdFunc(flags cli.MountFlags, args []string) error {
	source := args[0]
	mountName, err := mountNameFromArgs(args)
	if err != nil {
		return err
	}

	mountType := flags.Type
	// Check the appropriate mounter is installed by stating /sbin/mount.<type>.
	if err := checkMountCommand(mountType); err != nil {
		return err
	}

	opts := flags.Opts
	target := filepath.Join(e.s.cfg.MountsRoot, mountName)
	dv, err := e.s.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get services: %w", err)
	}
	if dv.Volumes().Contains(mountName) {
		return fmt.Errorf("volume %q already exists; please remove it first", mountName)
	}
	deps := flags.Deps
	d := dv.AsStruct()
	vol := db.Volume{
		Name: mountName,
		Src:  source,
		Path: target,
		Type: mountType,
		Opts: opts,
		Deps: strings.Join(deps, " "),
	}
	mak.Set(&d.Volumes, mountName, &vol)
	if err := e.s.cfg.DB.Set(d); err != nil {
		return fmt.Errorf("failed to save data: %w", err)
	}
	if err := mountVolume(vol); err != nil {
		return fmt.Errorf("failed to mount %s at %s: %w", source, target, err)
	}

	if _, err := fmt.Fprintf(e.rw, "Mounted %s at %s\n", source, target); err != nil {
		return fmt.Errorf("failed to write mount result: %w", err)
	}
	return nil
}

func mountNameFromArgs(args []string) (string, error) {
	source := args[0]
	_, srcPath, ok := strings.Cut(source, ":")
	if !ok {
		return "", fmt.Errorf("source %q must be in the format host:path", source)
	}
	mountName := filepath.Base(srcPath)
	if len(args) == 2 {
		mountName = args[1]
	}
	if strings.Contains(mountName, "/") {
		return "", fmt.Errorf("target cannot contain a /")
	}
	return mountName, nil
}

func printMounts(w io.Writer, volumes map[string]*db.Volume) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tSRC\tPATH\tTYPE\tOPTS"); err != nil {
		return fmt.Errorf("failed to write mount header: %w", err)
	}
	for _, v := range volumes {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.Name, v.Src, v.Path, v.Type, v.Opts); err != nil {
			return fmt.Errorf("failed to write mount row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("failed to flush mounts: %w", err)
	}
	return nil
}

func (e *ttyExecer) tsCmdFunc(args []string) error {
	passthrough := len(args) > 0 && args[0] == "--"
	if passthrough {
		args = args[1:]
	}
	if e.sn == SystemService || e.sn == CatchService {
		return errors.New("tailscale command not supported for sys or catch service")
	}
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return fmt.Errorf("failed to get service view: %w", err)
	}
	if !sv.TSNet().Valid() {
		return errors.New("service is not connected to tailscale")
	}
	if !passthrough && len(args) > 0 && args[0] == "update" {
		return e.tsUpdateCmdFunc(sv, args[1:])
	}
	return e.runRawTailscaleCmd(sv, args)
}

func (e *ttyExecer) runRawTailscaleCmd(sv db.ServiceView, args []string) error {
	serviceRoot := e.s.serviceRootFromView(sv)
	sock := filepath.Join(serviceRunDirForRoot(serviceRoot), "tailscaled.sock")
	if _, err := os.Stat(sock); err != nil {
		return fmt.Errorf("tailscaled socket not found: %w", err)
	}
	ts, err := e.s.getTailscaleBinary(sv.TSNet().Version())
	if err != nil {
		return fmt.Errorf("failed to get tailscale binary: %w", err)
	}
	args = append([]string{
		"--socket=" + sock,
	}, args...)
	c := e.newCmd(ts, args...)
	if err := c.Run(); err != nil {
		return fmt.Errorf("failed to run tailscale command: %w", err)
	}
	return nil
}

var (
	tailscaleLatestVersionForTrackFn = tailscaleLatestVersionForTrack
	tailscaleTrackHTTPClient         = http.DefaultClient
	tailscaleTrackMetaURL            = func(track string) string {
		return fmt.Sprintf("https://pkgs.tailscale.com/%s/?mode=json", track)
	}
)

type tailscaleTrackMeta struct {
	TarballsVersion string `json:"TarballsVersion"`
}

func parseTSUpdateTarget(args []string) (target string, pinned bool, err error) {
	if len(args) == 0 {
		return "", false, nil
	}
	if len(args) == 1 {
		arg := strings.TrimSpace(args[0])
		if strings.HasPrefix(arg, "--version=") {
			target = strings.TrimSpace(strings.TrimPrefix(arg, "--version="))
		} else if strings.HasPrefix(arg, "--") {
			return "", false, fmt.Errorf("unknown yeet tailscale update flag %q; run `yeet ts <svc> -- update ...` for the official subcommand", arg)
		} else {
			target = arg
		}
	} else if len(args) == 2 && strings.TrimSpace(args[0]) == "--version" {
		target = strings.TrimSpace(args[1])
	} else {
		return "", false, fmt.Errorf("yeet tailscale update accepts at most one version target; run `yeet ts <svc> -- update ...` for the official subcommand")
	}
	if target == "" {
		return "", false, errors.New("update version cannot be empty")
	}
	v, err := semver.NewVersion(target)
	if err != nil {
		return "", false, fmt.Errorf("invalid pinned tailscale version %q: %w", target, err)
	}
	return v.String(), true, nil
}

func tailscaleTrackFromVersion(ver string) (string, error) {
	v, err := semver.NewVersion(strings.TrimSpace(ver))
	if err != nil {
		return "", fmt.Errorf("invalid tailscale version %q: %w", ver, err)
	}
	if v.Minor()%2 == 1 {
		return "unstable", nil
	}
	return "stable", nil
}

func tailscaleLatestVersionForTrack(track string) (string, error) {
	track, err := normalizeTailscaleTrack(track)
	if err != nil {
		return "", err
	}
	resp, err := tailscaleTrackHTTPClient.Get(tailscaleTrackMetaURL(track))
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	return tailscaleTrackVersionFromResponse(resp)
}

func normalizeTailscaleTrack(track string) (string, error) {
	track = strings.TrimSpace(track)
	if track != "stable" && track != "unstable" {
		return "", fmt.Errorf("invalid tailscale track %q", track)
	}
	return track, nil
}

func tailscaleTrackVersionFromResponse(resp *http.Response) (string, error) {
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tailscale package lookup failed: %s", resp.Status)
	}
	var meta tailscaleTrackMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", err
	}
	return tailscaleTrackVersionFromMeta(meta)
}

func tailscaleTrackVersionFromMeta(meta tailscaleTrackMeta) (string, error) {
	ver := strings.TrimSpace(meta.TarballsVersion)
	if ver == "" {
		return "", errors.New("tailscale package lookup returned empty version")
	}
	if _, err := semver.NewVersion(ver); err != nil {
		return "", fmt.Errorf("tailscale package lookup returned invalid version %q: %w", ver, err)
	}
	return ver, nil
}

func confirmTSUpdate(rw io.ReadWriter, from, to string) (bool, error) {
	if _, err := fmt.Fprintf(rw, "This will update Tailscale from %s to %s. Continue? [y/n] ", from, to); err != nil {
		return false, fmt.Errorf("failed to write update confirmation prompt: %w", err)
	}
	var confirm string
	_, err := fmt.Fscanln(rw, &confirm)
	if err != nil {
		if errors.Is(err, io.EOF) || err.Error() == "unexpected newline" {
			return false, nil
		}
		return false, fmt.Errorf("failed to read update confirmation: %w", err)
	}
	return strings.EqualFold(strings.TrimSpace(confirm), "y"), nil
}

func (e *ttyExecer) tsUpdateCmdFunc(sv db.ServiceView, args []string) error {
	current, track, latest, pinned, err := resolveTSUpdate(sv, args)
	if err != nil {
		return err
	}

	if err := printTSUpdateIntro(e.rw, current, track, latest, pinned); err != nil {
		return err
	}
	if latest == current {
		return printTSUpdateStatus(e.rw, "Already up to date (%s)\n", current)
	}
	ok, err := confirmTSUpdate(e.rw, current, latest)
	if err != nil {
		return err
	}
	if !ok {
		return printTSUpdateStatus(e.rw, "Update canceled.\n")
	}
	return e.applyTSUpdate(current, latest)
}

func resolveTSUpdate(sv db.ServiceView, args []string) (current, track, latest string, pinned bool, err error) {
	target, pinned, err := parseTSUpdateTarget(args)
	if err != nil {
		return "", "", "", false, err
	}
	current = strings.TrimSpace(sv.TSNet().Version())
	if current == "" {
		return "", "", "", false, errors.New("service tailscale version is not set")
	}
	track, err = tailscaleTrackFromVersion(current)
	if err != nil {
		return "", "", "", false, err
	}
	latest = target
	if !pinned {
		latest, err = tailscaleLatestVersionForTrackFn(track)
		if err != nil {
			return "", "", "", false, fmt.Errorf("failed to resolve latest tailscale version for %s track: %w", track, err)
		}
	}
	return current, track, latest, pinned, nil
}

func (e *ttyExecer) applyTSUpdate(current, latest string) error {
	tsd, err := e.s.getTailscaledBinary(latest)
	if err != nil {
		return fmt.Errorf("failed to download tailscaled %s: %w", latest, err)
	}
	if _, err := e.s.getTailscaleBinary(latest); err != nil {
		return fmt.Errorf("failed to download tailscale %s: %w", latest, err)
	}
	serviceRoot, err := e.s.serviceRootDir(e.sn)
	if err != nil {
		return fmt.Errorf("failed to resolve service root: %w", err)
	}
	runBinary := filepath.Join(serviceRunDirForRoot(serviceRoot), "tailscaled")
	if err := fileutil.CopyFile(tsd, runBinary); err != nil {
		return fmt.Errorf("failed to replace tailscaled binary: %w", err)
	}
	if err := e.persistTSUpdate(latest, tsd); err != nil {
		return fmt.Errorf("failed to persist tailscale version update: %w", err)
	}
	unit := fmt.Sprintf("yeet-%s-ts.service", e.sn)
	if err := e.newCmd("systemctl", "restart", unit).Run(); err != nil {
		return fmt.Errorf("failed to restart tailscaled service: %w", err)
	}
	if _, err := fmt.Fprintf(e.rw, "Updated tailscale for %s: %s -> %s\n", e.sn, current, latest); err != nil {
		return fmt.Errorf("failed to write tailscale update result: %w", err)
	}
	return nil
}

func (e *ttyExecer) persistTSUpdate(latest, tsd string) error {
	_, _, err := e.s.cfg.DB.MutateService(e.sn, func(_ *db.Data, s *db.Service) error {
		if s.TSNet == nil {
			return errors.New("service is not connected to tailscale")
		}
		s.TSNet.Version = latest
		art := ensureTSBinaryArtifact(s)
		art.Refs[db.ArtifactRef("latest")] = tsd
		art.Refs[db.Gen(s.Generation)] = tsd
		return nil
	})
	return err
}

func ensureTSBinaryArtifact(s *db.Service) *db.Artifact {
	if s.Artifacts == nil {
		s.Artifacts = db.ArtifactStore{}
	}
	art, ok := s.Artifacts[db.ArtifactTSBinary]
	if !ok || art == nil {
		art = &db.Artifact{Refs: map[db.ArtifactRef]string{}}
		mak.Set(&s.Artifacts, db.ArtifactTSBinary, art)
	}
	if art.Refs == nil {
		art.Refs = map[db.ArtifactRef]string{}
	}
	return art
}

func printTSUpdateStatus(w io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return fmt.Errorf("failed to write tailscale update status: %w", err)
	}
	return nil
}

func printTSUpdateIntro(w io.Writer, current, track, latest string, pinned bool) error {
	if _, err := fmt.Fprintln(w, "Running yeet-managed tailscale update (not official tailscale update)."); err != nil {
		return fmt.Errorf("failed to write tailscale update status: %w", err)
	}
	if _, err := fmt.Fprintf(w, "Current version: %s (%s track)\n", current, track); err != nil {
		return fmt.Errorf("failed to write tailscale update status: %w", err)
	}
	if pinned {
		if _, err := fmt.Fprintf(w, "Pinned target version: %s\n", latest); err != nil {
			return fmt.Errorf("failed to write tailscale update status: %w", err)
		}
	}
	return nil
}

func (e *ttyExecer) ipCmdFunc() error {
	if e.sn == CatchService {
		return e.printCatchTailscaleIPs()
	}
	if handled, err := e.printVMIPs(); handled {
		return err
	}
	if e.sn != SystemService {
		return e.printServiceEndpointIPs()
	}

	args, err := e.ipListArgs()
	if err != nil {
		return err
	}
	ips, err := listIPv4AddrsFn(args)
	if err != nil {
		return fmt.Errorf("failed to get IP addresses: %w", err)
	}
	if err := printIfaceIPs(e.rw, ips); err != nil {
		return fmt.Errorf("failed to write IP address: %w", err)
	}
	return nil
}

func (e *ttyExecer) printServiceEndpointIPs() error {
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return fmt.Errorf("failed to get service view: %w", err)
	}
	ctx := e.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ips, err := e.s.serviceIPListWithContext(ctx, e.sn, sv)
	if err != nil {
		return fmt.Errorf("failed to get IP addresses: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("service %q has no connectable IP endpoints", e.sn)
	}
	if err := printServiceIPs(e.rw, ips); err != nil {
		return fmt.Errorf("failed to write IP address: %w", err)
	}
	return nil
}

func (e *ttyExecer) printVMIPs() (bool, error) {
	if e.sn == SystemService {
		return false, nil
	}
	sv, err := e.s.serviceView(e.sn)
	if err != nil || sv.ServiceType() != db.ServiceTypeVM {
		return false, nil
	}
	ctx := e.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ips, err := e.s.serviceIPListWithContext(ctx, e.sn, sv)
	if err != nil {
		return true, fmt.Errorf("VM %q has no current IP known: %w", e.sn, err)
	}
	if len(ips) == 0 {
		return true, fmt.Errorf("VM %q has no current IP known", e.sn)
	}
	for _, ip := range ips {
		if _, err := fmt.Fprintln(e.rw, ip.IP); err != nil {
			return true, fmt.Errorf("failed to write IP address: %w", err)
		}
	}
	return true, nil
}

func (e *ttyExecer) printCatchTailscaleIPs() error {
	if e.s.cfg.LocalClient == nil {
		return errors.New("tailscale client unavailable")
	}
	st, err := e.s.cfg.LocalClient.StatusWithoutPeers(e.ctx)
	if err != nil {
		return fmt.Errorf("failed to get IP address: %w", err)
	}
	if err := printLines(e.rw, st.TailscaleIPs); err != nil {
		return fmt.Errorf("failed to write IP address: %w", err)
	}
	return nil
}

func (e *ttyExecer) ipListArgs() ([]string, error) {
	if e.sn == SystemService {
		args, _ := serviceIPListArgs(e.sn, db.ServiceView{})
		return args, nil
	}
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return nil, fmt.Errorf("failed to get service view: %w", err)
	}
	args, _ := serviceIPListArgs(e.sn, sv)
	return args, nil
}

func printIfaceIPs(w io.Writer, ips []ifaceIP) error {
	for _, ip := range ips {
		if _, err := fmt.Fprintln(w, ip.IP); err != nil {
			return err
		}
	}
	return nil
}

func printServiceIPs(w io.Writer, ips []catchrpc.ServiceIP) error {
	for _, ip := range ips {
		if _, err := fmt.Fprintln(w, ip.IP); err != nil {
			return err
		}
	}
	return nil
}

func printLines[T any](w io.Writer, lines []T) error {
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}
