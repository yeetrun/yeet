// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/shayne/yeet/pkg/cli"
	"github.com/shayne/yeet/pkg/db"
	"github.com/shayne/yeet/pkg/svc"
	"tailscale.com/util/mak"
)

func (e *ttyExecer) dockerCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("docker requires a subcommand")
	}
	subcmd := args[0]
	args = args[1:]
	if len(args) > 0 {
		return fmt.Errorf("docker %s takes no arguments", subcmd)
	}
	switch subcmd {
	case "pull":
		return e.dockerPullCmdFunc()
	case "update":
		return e.dockerUpdateCmdFunc()
	default:
		return fmt.Errorf("unknown docker command %q", subcmd)
	}
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
	if err := docker.Update(); err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep("")
	return nil
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
	if len(args) != 1 {
		return fmt.Errorf("invalid number of arguments")
	}
	mountName := args[0]
	dv, err := e.s.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get services: %w", err)
	}
	vol, ok := dv.Volumes().GetOk(mountName)
	if !ok {
		return fmt.Errorf("volume %q not found", mountName)
	}
	m := &systemdMounter{e: e, v: *vol.AsStruct()}
	if err := m.umount(); err != nil {
		return fmt.Errorf("failed to umount %s: %w", vol.Path(), err)
	}

	d := dv.AsStruct()
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
		tw := tabwriter.NewWriter(e.rw, 0, 0, 3, ' ', 0)
		defer tw.Flush()
		fmt.Fprintln(tw, "NAME\tSRC\tPATH\tTYPE\tOPTS")
		for _, v := range dv.AsStruct().Volumes {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.Name, v.Src, v.Path, v.Type, v.Opts)
		}
		return nil
	}
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("invalid number of arguments")
	}
	source := args[0]
	_, srcPath, ok := strings.Cut(source, ":")
	if !ok {
		return fmt.Errorf("source %q must be in the format host:path", source)
	}
	var mountName string
	if len(args) == 1 {
		mountName = filepath.Base(srcPath)
	} else {
		mountName = args[1]
	}

	if strings.Contains(mountName, "/") {
		return fmt.Errorf("target cannot contain a /")
	}

	mountType := flags.Type
	// Check the appropriate mounter is installed by stating /sbin/mount.<type>.
	mountCmd := fmt.Sprintf("/sbin/mount.%s", mountType)
	if _, err := os.Stat(mountCmd); err != nil {
		return fmt.Errorf("mount command %q not found", mountCmd)
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
	m := &systemdMounter{v: vol}

	if err := m.mount(); err != nil {
		return fmt.Errorf("failed to mount %s at %s: %w", source, target, err)
	}

	fmt.Fprintf(e.rw, "Mounted %s at %s\n", source, target)
	return nil
}

func (e *ttyExecer) tsCmdFunc(args []string) error {
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
	sock := filepath.Join(e.s.serviceRunDir(e.sn), "tailscaled.sock")
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

func (e *ttyExecer) ipCmdFunc() error {
	if e.sn == CatchService {
		st, err := e.s.cfg.LocalClient.StatusWithoutPeers(e.ctx)
		if err != nil {
			return fmt.Errorf("failed to get IP address: %w", err)
		}
		for _, ip := range st.TailscaleIPs {
			fmt.Fprintln(e.rw, ip)
		}
		return nil
	}

	args := []string{"-o", "-4", "addr", "list"}
	if e.sn != SystemService {
		sv, err := e.s.serviceView(e.sn)
		if err != nil {
			return fmt.Errorf("failed to get service view: %w", err)
		}
		if _, ok := sv.AsStruct().Artifacts.Gen(db.ArtifactNetNSService, sv.Generation()); ok {
			netns := fmt.Sprintf("yeet-%s-ns", e.sn)
			args = append([]string{"netns", "exec", netns, "ip"}, args...)
		}
	}
	ips, err := listIPv4Addrs(args)
	if err != nil {
		return fmt.Errorf("failed to get IP addresses: %w", err)
	}
	for _, ip := range ips {
		// Skip 127.0.0.1
		fmt.Fprintln(e.rw, ip.IP)
	}
	return nil
}
