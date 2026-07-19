// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/yeetrun/yeet/pkg/db"
	"tailscale.com/ipn"
	"tailscale.com/types/opt"
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

func (s *Server) reconcileTailscaleDNSConfigs(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}

	var errs []error
	for name, sv := range dv.Services().All() {
		if err := ctx.Err(); err != nil {
			return err
		}
		service := sv.AsStruct()
		restarted, err := reconcileTailscaleDNSConfig(service, s.serviceRootFromView(sv))
		if err != nil {
			log.Printf("tailscale DNS config reconciliation failed for service %q: %v", name, err)
			errs = append(errs, err)
			continue
		}
		if restarted {
			log.Printf("reconciled tailscale DNS config for service %q; restarted tailscale sidecar", name)
		}
	}
	return errors.Join(errs...)
}

func reconcileTailscaleDNSConfig(service *db.Service, serviceRoot string) (bool, error) {
	if _, ok := service.Artifacts.Gen(db.ArtifactTSService, service.Generation); !ok {
		return false, nil
	}

	configPaths := tailscaleDNSConfigPaths(service, serviceRoot)
	if len(configPaths) == 0 {
		return false, nil
	}

	var changed bool
	var errs []error
	for _, configPath := range configPaths {
		fileChanged, err := reconcileTailscaleDNSConfigFile(configPath)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		changed = changed || fileChanged
	}
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	return true, restartTailscaleSidecarForService(service.Name)
}

func tailscaleDNSConfigPaths(service *db.Service, serviceRoot string) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		paths = append(paths, path)
	}

	if configPath, ok := service.Artifacts.Gen(db.ArtifactTSConfig, service.Generation); ok {
		add(configPath)
	}
	if serviceRoot = strings.TrimSpace(serviceRoot); serviceRoot != "" {
		add(filepath.Join(serviceEnvDirForRoot(serviceRoot), "tailscaled.json"))
		// Keep reconciling the legacy runtime copy during upgrades until the
		// service has installed the root-owned env/ replacement.
		add(filepath.Join(serviceRunDirForRoot(serviceRoot), "tailscaled.json"))
	}
	return paths
}

func reconcileTailscaleDNSConfigFile(configPath string) (bool, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read tailscale config %s: %w", configPath, err)
	}
	var cfg ipn.ConfigVAlpha
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false, fmt.Errorf("parse tailscale config %s: %w", configPath, err)
	}
	if cfg.AcceptDNS.EqualBool(false) {
		return false, nil
	}

	cfg.AcceptDNS = opt.NewBool(false)
	next, err := json.Marshal(cfg)
	if err != nil {
		return false, fmt.Errorf("marshal tailscale config %s: %w", configPath, err)
	}
	if err := os.WriteFile(configPath, next, 0o644); err != nil {
		return false, fmt.Errorf("write tailscale config %s: %w", configPath, err)
	}
	return true, nil
}

func (s *Server) reconcileTailscaleResolverIsolation(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	repairs, errs, err := collectTailscaleResolverIsolationRepairs(ctx, dv)
	if err != nil {
		return err
	}
	if len(repairs) == 0 {
		return errors.Join(errs...)
	}
	errs = append(errs, applyTailscaleResolverIsolationRepairs(repairs)...)
	return errors.Join(errs...)
}

func collectTailscaleResolverIsolationRepairs(ctx context.Context, dv *db.DataView) ([]tailscaleResolverIsolationRepair, []error, error) {
	var errs []error
	var repairs []tailscaleResolverIsolationRepair
	for name, sv := range dv.Services().All() {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		service := sv.AsStruct()
		repair, err := prepareTailscaleResolverIsolationRepair(*service)
		if err != nil {
			log.Printf("tailscale resolver isolation reconciliation failed for service %q: %v", name, err)
			errs = append(errs, err)
			continue
		}
		if repair != nil {
			repairs = append(repairs, *repair)
		}
	}
	return repairs, errs, nil
}

func applyTailscaleResolverIsolationRepairs(repairs []tailscaleResolverIsolationRepair) []error {
	var errs []error
	var applied []tailscaleResolverIsolationRepair
	for _, repair := range repairs {
		if err := os.WriteFile(repair.installedPath, []byte(repair.next), 0o644); err != nil {
			errs = append(errs, fmt.Errorf("write tailscale unit %s: %w", repair.installedPath, err))
			continue
		}
		applied = append(applied, repair)
	}
	if len(applied) == 0 {
		return errs
	}

	if err := catchSystemctl("daemon-reload"); err != nil {
		errs = append(errs, fmt.Errorf("systemctl daemon-reload: %w", err))
		errs = append(errs, rollbackTailscaleResolverIsolationRepairs(applied)...)
		return errs
	}

	errs = append(errs, restartTailscaleResolverIsolationRepairs(applied)...)
	return errs
}

func restartTailscaleResolverIsolationRepairs(applied []tailscaleResolverIsolationRepair) []error {
	var errs []error
	for _, repair := range applied {
		if err := restartTailscaleSidecarForService(repair.serviceName); err != nil {
			errs = append(errs, err)
			if rollbackErr := rollbackTailscaleResolverIsolationRepair(repair); rollbackErr != nil {
				errs = append(errs, rollbackErr)
			}
		}
	}
	return errs
}

type tailscaleResolverIsolationRepair struct {
	serviceName   string
	installedPath string
	original      string
	next          string
}

func prepareTailscaleResolverIsolationRepair(service db.Service) (*tailscaleResolverIsolationRepair, error) {
	if _, ok := service.Artifacts.Gen(db.ArtifactTSService, service.Generation); !ok {
		return nil, nil
	}

	installedPath := tailscaleSidecarInstalledUnitPath(service.Name)
	raw, err := os.ReadFile(installedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tailscale unit %s: %w", installedPath, err)
	}
	next, changed := ensureTailscaleUnitResolverIsolation(string(raw))
	if !changed {
		return nil, nil
	}
	return &tailscaleResolverIsolationRepair{
		serviceName:   service.Name,
		installedPath: installedPath,
		original:      string(raw),
		next:          next,
	}, nil
}

func tailscaleSidecarInstalledUnitPath(serviceName string) string {
	return filepath.Join(systemdSystemDir, "yeet-"+serviceName+"-ts.service")
}

func rollbackTailscaleResolverIsolationRepairs(repairs []tailscaleResolverIsolationRepair) []error {
	var errs []error
	for _, repair := range repairs {
		if err := rollbackTailscaleResolverIsolationRepair(repair); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func rollbackTailscaleResolverIsolationRepair(repair tailscaleResolverIsolationRepair) error {
	if err := os.WriteFile(repair.installedPath, []byte(repair.original), 0o644); err != nil {
		return fmt.Errorf("restore tailscale unit %s after failed resolver isolation repair: %w", repair.installedPath, err)
	}
	return nil
}

func ensureTailscaleUnitResolverIsolation(unit string) (string, bool) {
	netNS, ok := tailscaleUnitNetworkNamespace(unit)
	if !ok {
		return unit, false
	}

	bind := fmt.Sprintf("BindPaths=/etc/netns/%s/resolv.conf:/etc/resolv.conf", netNS)
	hasBind := systemdUnitHasDirective(unit, bind)
	hasPrivateMounts := systemdUnitHasDirective(unit, "PrivateMounts=yes")
	if hasBind && hasPrivateMounts {
		return unit, false
	}

	var insert []string
	if !hasBind {
		insert = append(insert, bind)
	}
	if !hasPrivateMounts {
		insert = append(insert, "PrivateMounts=yes")
	}
	return insertSystemdServiceDirectives(unit, insert), true
}

func tailscaleUnitNetworkNamespace(unit string) (string, bool) {
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		value, ok := strings.CutPrefix(line, "NetworkNamespacePath=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if value == "" {
			return "", false
		}
		netNS := filepath.Base(filepath.Clean(value))
		if netNS == "." || netNS == string(filepath.Separator) {
			return "", false
		}
		return netNS, true
	}
	return "", false
}

func systemdUnitHasDirective(unit, directive string) bool {
	for _, line := range strings.Split(unit, "\n") {
		if strings.TrimSpace(line) == directive {
			return true
		}
	}
	return false
}

func insertSystemdServiceDirectives(unit string, directives []string) string {
	lines := strings.Split(unit, "\n")
	var out []string
	inserted := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "[Install]" && !inserted {
			out = append(out, directives...)
			out = append(out, "")
			inserted = true
		}
		out = append(out, line)
	}
	if !inserted {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, directives...)
	}
	return strings.Join(out, "\n")
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
