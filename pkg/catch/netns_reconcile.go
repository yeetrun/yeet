// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/yeetrun/yeet/pkg/db"
)

type dockerNetNSReconciler interface {
	ReconcileNetNS(ctx context.Context) (bool, error)
}

var (
	tailscaleSidecarNetNSStale = tailscaleSidecarNetNSStaleOnHost
	tailscaleSidecarMainPID    = systemdMainPID
	statNetNSPath              = os.Stat
)

func (s *Server) reconcileNetNSBackedDockerServices(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}

	var errs []error
	for name, sv := range dv.Services().All() {
		if err := ctx.Err(); err != nil {
			return err
		}
		restarted, err := s.reconcileNetNSBackedDockerService(ctx, name, sv)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			log.Printf("netns reconciliation failed for service %q: %v", name, err)
			errs = append(errs, err)
			continue
		}
		if restarted {
			log.Printf("reconciled stale docker netns for service %q; restarted containers", name)
		}
	}

	return errors.Join(errs...)
}

func (s *Server) reconcileNetNSBackedDockerService(ctx context.Context, name string, sv db.ServiceView) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	serviceRecord := sv.AsStruct()
	if serviceRecord.ServiceType != db.ServiceTypeDockerCompose {
		return false, nil
	}
	if _, ok := serviceRecord.Artifacts.Gen(db.ArtifactNetNSService, serviceRecord.Generation); !ok {
		return false, nil
	}

	service, err := s.newDockerComposeService(sv)
	if err != nil {
		return false, fmt.Errorf("load docker compose service %q: %w", name, err)
	}
	restarted, err := service.ReconcileNetNS(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return false, err
		}
		return false, fmt.Errorf("reconcile docker compose service %q: %w", name, err)
	}
	if !restarted {
		if err := reconcileTailscaleSidecarAfterNetNSCheck(serviceRecord, false); err != nil {
			return false, fmt.Errorf("repair tailscale sidecar for docker compose service %q: %w", name, err)
		}
		return false, nil
	}
	if err := reconcileTailscaleSidecarAfterNetNSCheck(serviceRecord, true); err != nil {
		return false, fmt.Errorf("restart tailscale sidecar for docker compose service %q: %w", name, err)
	}
	return true, nil
}

func reconcileTailscaleSidecarAfterNetNSCheck(service *db.Service, netNSRecreated bool) error {
	if _, ok := service.Artifacts.Gen(db.ArtifactTSService, service.Generation); !ok {
		return nil
	}
	if !netNSRecreated {
		stale, err := tailscaleSidecarNetNSStale(service.Name)
		if err != nil {
			return err
		}
		if !stale {
			return nil
		}
	}
	return restartTailscaleSidecarForService(service.Name)
}

func restartTailscaleSidecarForService(name string) error {
	unit := fmt.Sprintf("yeet-%s-ts.service", name)
	if err := catchSystemctl("restart", unit); err != nil {
		return fmt.Errorf("systemctl restart %s: %w", unit, err)
	}
	log.Printf("restarted tailscale sidecar for service %q after docker netns reconciliation", name)
	return nil
}

func tailscaleSidecarNetNSStaleOnHost(name string) (bool, error) {
	unit := fmt.Sprintf("yeet-%s-ts.service", name)
	pid, err := tailscaleSidecarMainPID(unit)
	if err != nil {
		return false, err
	}
	if pid == 0 {
		return false, nil
	}

	procInfo, err := statNetNSPath(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat tailscale sidecar netns for %s: %w", unit, err)
	}
	namedInfo, err := statNetNSPath(fmt.Sprintf("/var/run/netns/yeet-%s-ns", name))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat named netns for %s: %w", name, err)
	}

	procInode, err := fileInode(procInfo)
	if err != nil {
		return false, fmt.Errorf("read tailscale sidecar netns inode for %s: %w", unit, err)
	}
	namedInode, err := fileInode(namedInfo)
	if err != nil {
		return false, fmt.Errorf("read named netns inode for %s: %w", name, err)
	}
	return procInode != namedInode, nil
}

func systemdMainPID(unit string) (int, error) {
	output, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", unit).Output()
	if err != nil {
		return 0, fmt.Errorf("systemctl show MainPID for %s: %w", unit, err)
	}
	text := strings.TrimSpace(string(output))
	if text == "" || text == "0" {
		return 0, nil
	}
	pid, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("parse MainPID for %s: %w", unit, err)
	}
	return pid, nil
}

func fileInode(info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unexpected file info type %T", info.Sys())
	}
	return stat.Ino, nil
}
