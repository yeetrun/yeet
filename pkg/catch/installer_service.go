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
	"log"
	"math/rand/v2"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/util/set"
)

// NewInstaller returns a new SystemdInstaller for the given service
// name. The binary will be stored in the service's bin directory and installed
// as a service when closed.
func (s *Server) NewInstaller(cfg InstallerCfg) (*Installer, error) {
	si := &Installer{
		icfg: cfg,
		s:    s,

		NewCmd: cmdutil.NewStdCmd,
	}
	return si, nil
}

// Installer is an io.WriteCloser that writes the received binary to a file and
// installs the service when closed.
type Installer struct {
	NewCmd func(name string, arg ...string) *exec.Cmd

	icfg                InstallerCfg
	s                   *Server
	committedGeneration int
	isoComposeInstall   func(*db.Service) error
	isoTailscaleAuthKey string
}

type isoTailscaleInstallFunc func(string, string, string, *db.TailscaleNetwork, string, string) (map[db.ArtifactName]string, error)

//nolint:cyclop // Installation and generation-checked persistence form one ordered transaction.
func (si *Installer) installISOTailscale(ctx context.Context, service *db.Service, install isoTailscaleInstallFunc) error {
	if service == nil || service.ISO == nil || service.TSNet == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateISOTailscaleAllocation(service.Name, service.ISO); err != nil {
		return err
	}
	if install == nil {
		install = si.s.installISOServiceTSAtRoot
	}
	serviceRoot := si.s.serviceRootFromView(service.View())
	resolverPath := filepath.Join(serviceBinDirForRoot(serviceRoot), fmt.Sprintf("iso-resolv.gen-%d.conf", service.Generation))
	if err := os.WriteFile(resolverPath, []byte("nameserver "+service.ISO.Gateway.String()+"\n"), 0o644); err != nil {
		return fmt.Errorf("write ISO Tailscale resolver: %w", err)
	}
	artifacts, err := install(serviceRoot, service.Name, service.ISO.NetNS, service.TSNet.Clone(), si.isoTailscaleAuthKey, resolverPath)
	if err != nil {
		return fmt.Errorf("install ISO Tailscale sidecar: %w", err)
	}
	if artifacts == nil {
		artifacts = map[db.ArtifactName]string{}
	}
	artifacts[db.ArtifactNetNSResolv] = resolverPath
	_, _, err = si.s.cfg.DB.MutateService(service.Name, func(_ *db.Data, record *db.Service) error {
		if record.Generation != service.Generation || record.ISO == nil {
			return fmt.Errorf("service %q changed during ISO Tailscale installation", service.Name)
		}
		for name, path := range artifacts {
			artifact := record.Artifacts[name]
			if artifact == nil {
				artifact = &db.Artifact{Refs: map[db.ArtifactRef]string{}}
				if record.Artifacts == nil {
					record.Artifacts = db.ArtifactStore{}
				}
				record.Artifacts[name] = artifact
			}
			if artifact.Refs == nil {
				artifact.Refs = map[db.ArtifactRef]string{}
			}
			artifact.Refs[db.Gen(service.Generation)] = path
			artifact.Refs["latest"] = path
		}
		return nil
	})
	return err
}

var liveSvcNetworkIPsFunc = liveSvcNetworkIPs

func unassignedIP(dv db.DataView) (netip.Addr, error) {
	assigned := assignedSvcNetworkIPs(dv)
	live, err := liveSvcNetworkIPsFunc()
	if err != nil {
		log.Printf("failed to inspect live service network IPs: %v", err)
	} else {
		for ip := range live {
			assigned[ip] = true
		}
	}
	ip := netip.MustParseAddr("192.168.100.3")
	pfx := netip.MustParsePrefix("192.168.100.0/24")
	max := netip.MustParseAddr("192.168.100.253")
	for assigned[ip] && ip.Less(max) {
		ip = ip.Next()
	}
	if !pfx.Contains(ip) || ip.Compare(max) > 0 {
		return netip.Addr{}, fmt.Errorf("no available IP address")
	}
	return ip, nil
}

func assignedSvcNetworkIPs(dv db.DataView) map[netip.Addr]bool {
	assigned := map[netip.Addr]bool{}
	for _, s := range dv.AsStruct().Services {
		if s.SvcNetwork != nil && s.SvcNetwork.IPv4.IsValid() {
			assigned[s.SvcNetwork.IPv4] = true
		}
	}
	return assigned
}

func liveSvcNetworkIPs() (map[netip.Addr]bool, error) {
	out := map[netip.Addr]bool{}
	raw, err := exec.Command("ip", "netns", "list").Output()
	if err != nil {
		return out, err
	}
	pfx := netip.MustParsePrefix("192.168.100.0/24")
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		addrRaw, err := exec.Command("ip", "netns", "exec", fields[0], "ip", "-o", "-4", "addr", "show", "scope", "global").Output()
		if err != nil {
			continue
		}
		parseLiveSvcNetworkIPs(out, pfx, addrRaw)
	}
	return out, nil
}

func parseLiveSvcNetworkIPs(out map[netip.Addr]bool, pfx netip.Prefix, raw []byte) {
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field != "inet" || i+1 >= len(fields) {
				continue
			}
			addr, err := netip.ParsePrefix(fields[i+1])
			if err == nil && pfx.Contains(addr.Addr()) {
				out[addr.Addr()] = true
			}
		}
	}
}

func randomMAC() string {
	var b [6]byte
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	// Ensure the address is unicast and locally administered
	b[0] = (b[0] & 0xfe) | 0x02

	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

const maxGenerations = 10

func (si *Installer) mutateService(f func(*db.Data, *db.Service) error) (*db.Data, *db.Service, error) {
	return si.s.cfg.DB.MutateService(si.icfg.ServiceName, f)
}

func (si *Installer) commitGen(gen int) (*db.Data, *db.Service, error) {
	return si.commitGenWithExpected(gen, nil)
}

func (si *Installer) commitGenIfCurrent(expected, gen int) (*db.Data, *db.Service, error) {
	return si.commitGenWithExpected(gen, &expected)
}

func (si *Installer) commitGenWithExpected(gen int, expected *int) (*db.Data, *db.Service, error) {
	d, s, err := si.mutateService(func(d *db.Data, s *db.Service) error {
		if expected != nil && s.Generation != *expected {
			return fmt.Errorf("service generation changed from expected %d to %d", *expected, s.Generation)
		}
		commitGeneratedServiceRefs(d, s, si.icfg.ServiceName, generatedServiceCommitForGen(gen, s.LatestGeneration))
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to commit generation: %v", err)
	}
	return d, s, nil
}

type generatedServiceCommit struct {
	srcRef           string
	dstRefs          []string
	generation       int
	latestGeneration int
}

func generatedServiceCommitForGen(gen, latestGeneration int) generatedServiceCommit {
	if gen == 0 {
		next := latestGeneration + 1
		return generatedServiceCommit{
			srcRef:           "staged",
			dstRefs:          []string{"latest", string(db.Gen(next))},
			generation:       next,
			latestGeneration: next,
		}
	}
	return generatedServiceCommit{
		srcRef:           string(db.Gen(gen)),
		dstRefs:          []string{"latest"},
		generation:       gen,
		latestGeneration: latestGeneration,
	}
}

func commitGeneratedServiceRefs(d *db.Data, s *db.Service, serviceName string, commit generatedServiceCommit) {
	s.LatestGeneration = commit.latestGeneration
	s.Generation = commit.generation
	commitArtifactRefs(s.Artifacts, commit)
	if d != nil {
		commitImageRefs(d.Images, serviceName, commit)
	}
}

func commitArtifactRefs(artifacts db.ArtifactStore, commit generatedServiceCommit) {
	for _, refs := range artifacts {
		if refs == nil {
			continue
		}
		val, ok := refs.Refs[db.ArtifactRef(commit.srcRef)]
		if !ok {
			continue
		}
		for _, ref := range commit.dstRefs {
			refs.Refs[db.ArtifactRef(ref)] = val
		}
	}
}

func commitImageRefs(images map[db.ImageRepoName]*db.ImageRepo, serviceName string, commit generatedServiceCommit) {
	for rn, ir := range images {
		if imageRepoServiceName(rn) != serviceName {
			log.Printf("skipping image %q", rn)
			continue
		}
		if ir == nil {
			continue
		}
		val, ok := ir.Refs[db.ImageRef(commit.srcRef)]
		if !ok {
			log.Printf("image %v:%v not found", rn, commit.srcRef)
			continue
		}
		for _, ref := range commit.dstRefs {
			log.Printf("setting image %v:%v to %v:%v", rn, commit.srcRef, rn, ref)
			ir.Refs[db.ImageRef(ref)] = val
		}
	}
}

func imageRepoServiceName(rn db.ImageRepoName) string {
	serviceName, _, _ := strings.Cut(string(rn), "/")
	return serviceName
}

func parseGenRef(ref db.ArtifactRef) (int, bool) {
	genStr, ok := strings.CutPrefix(string(ref), "gen-")
	if !ok {
		return 0, false
	}
	gen, err := strconv.Atoi(genStr)
	if err != nil {
		return 0, false
	}
	return gen, true
}

// Prune removes old configurations from the database.
func (si *Installer) prune() {
	knownBins := defaultKnownInstallFiles(si.icfg.ServiceName)
	serviceRoot := si.s.defaultServiceRootDir(si.icfg.ServiceName)
	_, _, err := si.mutateService(func(d *db.Data, s *db.Service) error {
		serviceRoot = si.s.serviceRootFromView(s.View())
		pruneServiceArtifacts(s, knownBins)
		return nil
	})
	if err != nil {
		log.Printf("failed to mutate service: %v", err)
		return
	}

	si.pruneInstallDirectories(serviceRoot, knownBins)
}

func defaultKnownInstallFiles(serviceName string) set.Set[string] {
	knownFiles := make(set.Set[string])
	// TODO(maisem): this should not be hardcoded here.
	knownFiles.AddSlice([]string{"netns.env", "env", "main.ts", serviceName})
	return knownFiles
}

func pruneServiceArtifacts(s *db.Service, knownFiles set.Set[string]) {
	minGen := s.LatestGeneration - maxGenerations
	for _, refs := range s.Artifacts {
		pruneArtifactRefs(refs, minGen, knownFiles)
	}
}

func pruneArtifactRefs(refs *db.Artifact, minGen int, knownFiles set.Set[string]) {
	if refs == nil {
		return
	}
	for ref, p := range refs.Refs {
		if shouldKeepArtifactRef(ref, minGen) {
			knownFiles.Add(filepath.Base(p))
			continue
		}
		delete(refs.Refs, ref)
	}
}

func shouldKeepArtifactRef(ref db.ArtifactRef, minGen int) bool {
	gen, ok := parseGenRef(ref)
	return !ok || gen >= minGen
}

func (si *Installer) pruneInstallDirectories(serviceRoot string, knownFiles set.Set[string]) {
	for _, dir := range []string{
		serviceBinDirForRoot(serviceRoot),
		serviceEnvDirForRoot(serviceRoot),
	} {
		if err := pruneInstallDirectory(dir, knownFiles); err != nil {
			log.Printf("failed to keep only known files in %q: %v", dir, err)
		}
	}
}

func pruneInstallDirectory(dir string, known set.Set[string]) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}
	for _, f := range files {
		if !known.Contains(f.Name()) {
			fp := filepath.Join(dir, f.Name())
			if err := os.Remove(fp); err != nil {
				log.Printf("failed to remove file: %v", err)
			} else {
				log.Printf("Removed old file: %s", fp)
			}
		}
	}
	return nil
}

/*
    TDDO: move to place where we write the file.
	isSelfUpdate := si.icfg.ServiceName == CatchService
	if isSelfUpdate && si.icfg.Artifact != "" {
		si.printf("Verifying catch binary\n")
		if err := verifyCatchBinary(si.icfg.Artifact); err != nil {
			si.printf("Failed to verify catch binary: %v\n", err)
			log.Printf("failed to verify catch binary: %v", err)
			return fmt.Errorf("failed to verify catch binary: %v", err)
		}
	}
*/

func (si *Installer) InstallGen(gen int) error {
	if runtime.GOOS == "darwin" {
		panic("macOS is not supported")
	}
	return si.installGen(gen)
}

func (si *Installer) installGen(gen int) error {
	return si.installGenWithExpected(gen, nil)
}

// InstallGenIfCurrent installs gen only when the service still has expected as
// its current generation at the database mutation that starts installation.
func (si *Installer) InstallGenIfCurrent(expected, gen int) error {
	if runtime.GOOS == "darwin" {
		panic("macOS is not supported")
	}
	return si.installGenIfCurrent(expected, gen)
}

func (si *Installer) installGenIfCurrent(expected, gen int) error {
	return si.installGenWithExpected(gen, &expected)
}

func (si *Installer) installGenWithExpected(gen int, expected *int) error {
	preService, err := si.serviceBeforeInstall()
	if err != nil {
		return err
	}
	if preService != nil {
		if err := validateInstallRequest(si.icfg.Pull, preService.ServiceType); err != nil {
			return err
		}
		if err := validateInstallNetworkRequest(preService); err != nil {
			return err
		}
	}

	operation := func() error {
		var d *db.Data
		var s *db.Service
		if expected == nil {
			d, s, err = si.commitGen(gen)
		} else {
			d, s, err = si.commitGenIfCurrent(*expected, gen)
		}
		if err != nil {
			return fmt.Errorf("failed to commit gen: %v", err)
		}
		si.committedGeneration = s.Generation
		si.prune()
		return si.doInstall(d, s)
	}
	if preService == nil {
		return operation()
	}
	return si.s.withServiceSnapshot(context.Background(), snapshotOperation{
		Service:   preService,
		Event:     snapshotEventRun,
		Writer:    si.snapshotWriter(),
		Operation: operation,
	})
}

// Install installs the service.
func (si *Installer) Install() error {
	return si.InstallGen(0)
}

func (si *Installer) doInstall(_ *db.Data, s *db.Service) error {
	if err := validateInstallRequest(si.icfg.Pull, s.ServiceType); err != nil {
		return err
	}
	if err := validateInstallNetworkRequest(s); err != nil {
		return err
	}
	if err := runInstallPhaseForSnapshot(si, s); err != nil {
		return err
	}
	si.publishInstallEvent(s)
	return nil
}

func (si *Installer) installDefinitionOnly(s *db.Service) error {
	if err := validateInstallRequest(si.icfg.Pull, s.ServiceType); err != nil {
		return err
	}
	if err := validateInstallNetworkRequest(s); err != nil {
		return err
	}
	if err := si.runDefinitionInstallPhase(s); err != nil {
		return err
	}
	si.publishInstallEvent(s)
	return nil
}

func (si *Installer) serviceBeforeInstall() (*db.Service, error) {
	if si == nil || si.s == nil {
		return nil, nil
	}
	sv, err := si.s.serviceView(si.icfg.ServiceName)
	if err != nil {
		if err == errServiceNotFound {
			return nil, nil
		}
		return nil, err
	}
	return sv.AsStruct(), nil
}

func validateInstallRequest(pull bool, serviceType db.ServiceType) error {
	if pull && serviceType != db.ServiceTypeDockerCompose {
		return fmt.Errorf("--pull is only valid for docker compose payloads")
	}
	return nil
}

func validateInstallNetworkRequest(service *db.Service) error {
	return validateInstallNetworkRequestWithPublished(service, false)
}

func validateInstallNetworkRequestWithPublished(service *db.Service, publishReset bool) error {
	if service == nil || service.ISO == nil {
		return nil
	}
	return iso.ValidateNetwork(iso.NetworkRequest{
		Payload:   networkPayloadKind(service.ServiceType),
		Modes:     service.ISO.DesiredModes,
		Published: len(service.Publish) != 0 || publishReset,
	})
}

func networkPayloadKind(serviceType db.ServiceType) iso.PayloadKind {
	switch serviceType {
	case db.ServiceTypeVM:
		return iso.PayloadVM
	case db.ServiceTypeDockerCompose:
		return iso.PayloadCompose
	// ISO intentionally rejects native root services. Host root can reconfigure or
	// leave a network namespace, so non-root systemd sandboxing is a prerequisite
	// for adding native ISO support without making a false security claim.
	case db.ServiceTypeSystemd:
		return iso.PayloadNative
	default:
		return iso.PayloadNative
	}
}

type installPhase func(*Installer, *db.Service) error

var runInstallPhaseForSnapshot = (*Installer).runInstallPhase

type printerFuncWriter struct {
	printer func(string, ...any)
}

func (si *Installer) snapshotWriter() io.Writer {
	if si.icfg.ClientOut != nil {
		return si.icfg.ClientOut
	}
	if si.icfg.Printer != nil {
		return printerFuncWriter{printer: si.icfg.Printer}
	}
	return nil
}

func (w printerFuncWriter) Write(p []byte) (int, error) {
	if w.printer != nil {
		w.printer("%s", string(p))
	}
	return len(p), nil
}

func (si *Installer) runInstallPhase(s *db.Service) error {
	phase, err := installPhaseForServiceType(s.ServiceType)
	if err != nil {
		return err
	}
	return phase(si, s)
}

func (si *Installer) runDefinitionInstallPhase(s *db.Service) error {
	phase, err := definitionInstallPhaseForServiceType(s.ServiceType)
	if err != nil {
		return err
	}
	return phase(si, s)
}

func installPhaseForServiceType(serviceType db.ServiceType) (installPhase, error) {
	switch serviceType {
	case db.ServiceTypeSystemd:
		return installSystemdService, nil
	case db.ServiceTypeDockerCompose:
		return installDockerComposeService, nil
	default:
		return nil, fmt.Errorf("unknown service type: %v", serviceType)
	}
}

func definitionInstallPhaseForServiceType(serviceType db.ServiceType) (installPhase, error) {
	switch serviceType {
	case db.ServiceTypeSystemd:
		return installSystemdServiceDefinition, nil
	case db.ServiceTypeDockerCompose:
		return installDockerComposeServiceDefinition, nil
	default:
		return nil, fmt.Errorf("unknown service type: %v", serviceType)
	}
}

func installSystemdService(si *Installer, s *db.Service) error {
	service, err := newSystemdInstallService(si, s)
	if err != nil {
		return err
	}
	if err := installSystemdUnit(service); err != nil {
		return err
	}
	closeSelfUpdateClient(si, s.Name)
	return restartSystemdUnit(service)
}

func installSystemdServiceDefinition(si *Installer, s *db.Service) error {
	service, err := newSystemdInstallService(si, s)
	if err != nil {
		return err
	}
	if err := installSystemdUnit(service); err != nil {
		return err
	}
	closeSelfUpdateClient(si, s.Name)
	return nil
}

func newSystemdInstallService(si *Installer, s *db.Service) (*svc.SystemdService, error) {
	serviceRoot := si.s.serviceRootFromView(s.View())
	service, err := svc.NewSystemdService(si.s.cfg.DB, s.View(), serviceRunDirForRoot(serviceRoot))
	if err != nil {
		return nil, fmt.Errorf("failed to create service: %v", err)
	}
	return service, nil
}

func installSystemdUnit(service *svc.SystemdService) error {
	if err := service.Install(); err != nil {
		return fmt.Errorf("failed to install service: %v", err)
	}
	return nil
}

func closeSelfUpdateClient(si *Installer, serviceName string) {
	if serviceName == CatchService && si.icfg.ClientCloser != nil {
		_ = si.icfg.ClientCloser.Close()
	}
}

func restartSystemdUnit(service *svc.SystemdService) error {
	if err := service.Restart(); err != nil {
		return fmt.Errorf("failed to restart service: %v", err)
	}
	return nil
}

func installDockerComposeService(si *Installer, s *db.Service) error {
	si.suspendUI()
	if s.ISO != nil {
		if si.isoComposeInstall != nil {
			return si.isoComposeInstall(s)
		}
		return si.installISOComposeService(s)
	}
	// Check that docker is installed before trying to install.
	if _, err := svc.DockerCmd(); err != nil {
		return err // svc.ErrDockerNotFound
	}
	service, err := si.newDockerComposeService(s)
	if err != nil {
		return fmt.Errorf("failed to create service: %v", err)
	}
	if err := service.InstallWithPull(si.icfg.Pull); err != nil {
		return fmt.Errorf("failed to install service: %v", err)
	}
	if err := service.UpWithPull(si.icfg.Pull); err != nil {
		return fmt.Errorf("failed to up service: %v", err)
	}
	return nil
}

type isoComposeLifecycle struct {
	si             *Installer
	record         *db.Service
	compose        *svc.DockerComposeService
	pullCompose    func(context.Context) error
	createCompose  func(context.Context) error
	readmitCompose func(context.Context) error
	downCompose    func(context.Context) error
	upCompose      func(context.Context) error
	startAux       func() error
	stopAux        func() error
	baseJSON       []byte
	mergedJSON     []byte
	base           ISOComposeModel
	merged         ISOComposeModel
	allocation     *db.ISOAllocation
	isoUnlock      func()
}

func (si *Installer) installISOComposeService(record *db.Service) error {
	if _, err := svc.DockerCmd(); err != nil {
		return err
	}
	compose, err := si.newDockerComposeService(record)
	if err != nil {
		return fmt.Errorf("load ISO Compose service: %w", err)
	}
	lifecycle := &isoComposeLifecycle{si: si, record: record.Clone(), compose: compose}
	loader := &FileInstaller{
		s: si.s,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: record.Name},
			Network:      NetworkOpts{Interfaces: strings.Join(record.ISO.DesiredModes, ","), Modes: slices.Clone(record.ISO.DesiredModes), ISO: true},
		},
		tsNet: record.TSNet.Clone(),
	}
	return loader.installISOCompose(context.Background(), lifecycle)
}

func (l *isoComposeLifecycle) resolveOptions(files []string) svc.ComposeResolveOptions {
	root := l.si.s.serviceRootFromView(l.record.View())
	return svc.ComposeResolveOptions{
		ProjectName: svc.ComposeProjectName(l.record.Name),
		ProjectDir:  serviceDataDirForRoot(root),
		Files:       slices.Clone(files),
		NewCmd:      l.si.newCommandContext,
	}
}

func (l *isoComposeLifecycle) baseComposePath() (string, error) {
	path, ok := l.record.Artifacts.Gen(db.ArtifactDockerComposeFile, l.record.Generation)
	if !ok {
		return "", fmt.Errorf("ISO Compose base artifact is missing for generation %d", l.record.Generation)
	}
	return path, nil
}

func (l *isoComposeLifecycle) overlayComposePath() (string, error) {
	path, ok := l.record.Artifacts.Gen(db.ArtifactDockerComposeNetwork, l.record.Generation)
	if !ok {
		return "", fmt.Errorf("ISO Compose overlay artifact is missing for generation %d", l.record.Generation)
	}
	return path, nil
}

func (l *isoComposeLifecycle) ResolveBase(ctx context.Context) error {
	path, err := l.baseComposePath()
	if err != nil {
		return err
	}
	l.baseJSON, err = svc.ResolveComposeJSON(ctx, l.resolveOptions([]string{path}))
	return err
}

func (l *isoComposeLifecycle) AdmitBase(context.Context) error {
	root := l.si.s.serviceRootFromView(l.record.View())
	model, err := AdmitISOCompose(l.baseJSON, ISOComposeAdmissionOptions{
		ServiceRoot: root, ProjectName: svc.ComposeProjectName(l.record.Name), MaxComponents: iso.MaxComponents,
	})
	l.base = model
	return err
}

func (l *isoComposeLifecycle) Reserve(ctx context.Context) error {
	return l.si.s.withISOOperationLock(ctx, func() error {
		allocation, err := l.si.s.reserveISOAllocation(ctx, l.record.Name, isoReservationRequest{
			Kind: iso.PayloadCompose, Modes: slices.Clone(l.record.ISO.DesiredModes), Components: slices.Clone(l.base.Components),
		})
		if err != nil {
			return err
		}
		l.allocation = allocation
		if len(allocation.RetiredComponents) != 0 {
			steps := &isoConcreteRetirementSteps{lifecycle: l}
			if err := l.si.s.finalizeISORetirementsWith(ctx, l.record.Name, steps); err != nil {
				return err
			}
			allocation, err = l.si.s.persistedISOAllocationForService(l.record.Name)
			if err != nil {
				return err
			}
			l.allocation = allocation
		}
		return nil
	})
}

func (s *Server) persistedISOAllocationForService(service string) (*db.ISOAllocation, error) {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return nil, err
	}
	view, ok := dv.Services().GetOk(service)
	if !ok || !view.ISO().Valid() {
		return nil, fmt.Errorf("service %q has no persisted ISO allocation", service)
	}
	return view.ISO().AsStruct(), nil
}

func (l *isoComposeLifecycle) RenderOverlay(context.Context) error {
	path, err := l.overlayComposePath()
	if err != nil {
		return err
	}
	content, err := renderISOComposeOverlay(l.allocation, l.base)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func (l *isoComposeLifecycle) ResolveMerged(ctx context.Context) error {
	base, err := l.baseComposePath()
	if err != nil {
		return err
	}
	overlay, err := l.overlayComposePath()
	if err != nil {
		return err
	}
	l.mergedJSON, err = svc.ResolveComposeJSON(ctx, l.resolveOptions([]string{base, overlay}))
	return err
}

func (l *isoComposeLifecycle) AdmitMerged(context.Context) error {
	root := l.si.s.serviceRootFromView(l.record.View())
	model, err := AdmitISOCompose(l.mergedJSON, ISOComposeAdmissionOptions{
		ServiceRoot: root, ProjectName: svc.ComposeProjectName(l.record.Name), MaxComponents: iso.MaxComponents, RequireISOOverlay: l.allocation,
	})
	if err != nil {
		return err
	}
	if !slices.Equal(l.base.Components, model.Components) {
		return fmt.Errorf("ISO overlay changed Compose components: base %v, merged %v", l.base.Components, model.Components)
	}
	l.merged = model
	return nil
}

func (l *isoComposeLifecycle) InstallDNS(context.Context) error {
	return installISODNSService(l.si.s.cfg.RootDir)
}

func (l *isoComposeLifecycle) runtimeSpec() (isoRuntimeNetworkSpec, error) {
	return l.si.s.loadISORuntimeSpec(l.record.Name)
}

func (l *isoComposeLifecycle) EnsurePolicy(ctx context.Context) error {
	unlock, err := acquireISOOperationLockForRuntime(ctx, l.si.s.cfg.RootDir)
	if err != nil {
		return err
	}
	l.isoUnlock = unlock
	spec, err := l.runtimeSpec()
	if err != nil {
		l.releaseISOLock()
		return err
	}
	rules, err := netns.RenderISOPolicy(spec.Backend, spec.Policy)
	if err != nil {
		l.releaseISOLock()
		return err
	}
	if err := netns.EnsureISOPolicy(ctx, rules); err != nil {
		l.releaseISOLock()
		return err
	}
	return nil
}

func (l *isoComposeLifecycle) VerifyPolicy(ctx context.Context) error {
	spec, err := l.runtimeSpec()
	if err != nil {
		l.releaseISOLock()
		return err
	}
	rules, err := netns.RenderISOPolicy(spec.Backend, spec.Policy)
	if err != nil {
		l.releaseISOLock()
		return err
	}
	if err := netns.VerifyISOPolicy(ctx, rules); err != nil {
		l.releaseISOLock()
		return err
	}
	return nil
}

func (l *isoComposeLifecycle) EnsureTopology(ctx context.Context) error {
	spec, err := l.runtimeSpec()
	if err != nil {
		l.releaseISOLock()
		return err
	}
	if err := netns.EnsureISOTopology(ctx, spec.Topology); err != nil {
		l.releaseISOLock()
		return err
	}
	return nil
}

func (l *isoComposeLifecycle) VerifyTopology(ctx context.Context) error {
	defer l.releaseISOLock()
	spec, err := l.runtimeSpec()
	if err != nil {
		return err
	}
	return netns.VerifyISOTopology(ctx, spec.Topology)
}

func (l *isoComposeLifecycle) releaseISOLock() {
	if l.isoUnlock == nil {
		return
	}
	l.isoUnlock()
	l.isoUnlock = nil
}

func (l *isoComposeLifecycle) InstallTailscale(ctx context.Context, _ *FileInstaller) error {
	if l.record.TSNet == nil {
		return nil
	}
	if err := l.si.installISOTailscale(ctx, l.record, nil); err != nil {
		return err
	}
	view, err := l.si.s.serviceView(l.record.Name)
	if err != nil {
		return err
	}
	l.record = view.AsStruct()
	l.compose, err = l.si.newDockerComposeService(l.record)
	return err
}

func (l *isoComposeLifecycle) Pull(ctx context.Context) error {
	if !l.si.icfg.Pull {
		return nil
	}
	if l.pullCompose != nil {
		return l.pullCompose(ctx)
	}
	return l.compose.PullContext(ctx)
}
func (l *isoComposeLifecycle) Build(context.Context) error { return nil }

func (l *isoComposeLifecycle) AttachNetwork(ctx context.Context) error {
	if err := l.reacquireAndVerifyForMutation(ctx); err != nil {
		return err
	}
	if l.createCompose != nil {
		return l.createCompose(ctx)
	}
	if err := l.compose.InstallDefinition(); err != nil {
		return err
	}
	return l.compose.Create(ctx)
}

func (l *isoComposeLifecycle) StartAux(ctx context.Context) error {
	// A gate unit can call back into iso-network-ensure, so never start it while
	// this process still owns the same host-wide ISO operation lock.
	l.releaseISOLock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.startAux != nil {
		return l.startAux()
	}
	return l.compose.StartAuxiliaryUnits()
}
func (l *isoComposeLifecycle) ComposeUp(ctx context.Context) error {
	if err := l.reacquireAndVerifyForStart(ctx); err != nil {
		return err
	}
	if l.upCompose != nil {
		return l.upCompose(ctx)
	}
	return l.compose.UpDetached(ctx, false)
}

func (l *isoComposeLifecycle) reacquireAndVerifyForStart(ctx context.Context) error {
	return l.reacquireAndVerifyCurrentBoundary(ctx, true)
}

func (l *isoComposeLifecycle) reacquireAndVerifyForMutation(ctx context.Context) error {
	return l.reacquireAndVerifyCurrentBoundary(ctx, true)
}

//nolint:cyclop // Lock reacquisition and boundary verification must remain visibly ordered.
func (l *isoComposeLifecycle) reacquireAndVerifyCurrentBoundary(ctx context.Context, readmit bool) error {
	unlock, err := acquireISOOperationLockForRuntime(ctx, l.si.s.cfg.RootDir)
	if err != nil {
		return err
	}
	l.isoUnlock = unlock
	fail := func(err error) error {
		l.releaseISOLock()
		return err
	}
	view, err := l.si.s.serviceView(l.record.Name)
	if err != nil {
		return fail(err)
	}
	current := view.AsStruct()
	if current.Generation != l.record.Generation || current.ISO == nil || current.ISO.RemoveRequested || !reflect.DeepEqual(current.ISO, l.allocation) {
		return fail(fmt.Errorf("service %q changed while the ISO network gate was starting", l.record.Name))
	}
	spec, err := l.runtimeSpec()
	if err != nil {
		return fail(err)
	}
	rules, err := netns.RenderISOPolicy(spec.Backend, spec.Policy)
	if err != nil {
		return fail(err)
	}
	if err := verifyISOPolicyForRuntime(ctx, rules); err != nil {
		return fail(err)
	}
	if err := verifyISOTopologyForRuntime(ctx, spec.Topology); err != nil {
		return fail(err)
	}
	if readmit {
		if err := l.revalidateComposeInputs(ctx); err != nil {
			return fail(err)
		}
	}
	return nil
}

func (l *isoComposeLifecycle) revalidateComposeInputs(ctx context.Context) error {
	if l.readmitCompose != nil {
		return l.readmitCompose(ctx)
	}
	if err := l.ResolveBase(ctx); err != nil {
		return fmt.Errorf("re-resolve base ISO Compose model: %w", err)
	}
	if err := l.AdmitBase(ctx); err != nil {
		return fmt.Errorf("re-admit base ISO Compose model: %w", err)
	}
	if err := l.ResolveMerged(ctx); err != nil {
		return fmt.Errorf("re-resolve merged ISO Compose model: %w", err)
	}
	if err := l.AdmitMerged(ctx); err != nil {
		return fmt.Errorf("re-admit merged ISO Compose model: %w", err)
	}
	return nil
}

func (l *isoComposeLifecycle) InspectRuntime(ctx context.Context) error {
	components := make(map[string]netip.Addr, len(l.allocation.Components))
	for name, component := range l.allocation.Components {
		components[name] = component.Address
	}
	base, err := l.baseComposePath()
	if err != nil {
		return err
	}
	overlay, err := l.overlayComposePath()
	if err != nil {
		return err
	}
	inspection, err := svc.InspectISOProject(ctx, svc.ISOInspectOptions{
		ProjectName: svc.ComposeProjectName(l.record.Name), ProjectDir: l.compose.DataDir,
		ComposeFiles: []string{base, overlay}, NetworkName: svc.ComposeProjectName(l.record.Name) + "_default",
		ServiceRoot: l.si.s.serviceRootFromView(l.record.View()), Components: components, NewCmd: l.si.newCommandContext,
	})
	if err != nil {
		return err
	}
	return inspection.Verify()
}

func (l *isoComposeLifecycle) MarkReady(context.Context) error {
	defer l.releaseISOLock()
	return l.si.s.markISOReady(l.record.Name)
}

func (l *isoComposeLifecycle) ComposeDownRemoveOrphans(ctx context.Context) error {
	l.releaseISOLock()
	down := l.downCompose
	if down == nil {
		down = l.compose.StopProjectContainers
	}
	stop := l.stopAux
	if stop == nil {
		stop = l.compose.StopAuxiliaryUnits
	}
	downErr := down(ctx)
	stopErr := stop()
	return errors.Join(downErr, stopErr)
}

func (l *isoComposeLifecycle) Quarantine(_ context.Context, cause error) error {
	l.releaseISOLock()
	return l.si.s.markISOState(l.record.Name, string(iso.StateQuarantined), cause)
}

type isoConcreteRetirementSteps struct{ lifecycle *isoComposeLifecycle }

func (r *isoConcreteRetirementSteps) StopProject(ctx context.Context, _ string) error {
	return r.lifecycle.compose.StopProjectContainers(ctx)
}
func (r *isoConcreteRetirementSteps) VerifyContainersAbsent(ctx context.Context, _ string, _ map[string]db.ISOComponent) error {
	return r.lifecycle.compose.VerifyProjectAbsent(ctx)
}
func (r *isoConcreteRetirementSteps) VerifyDockerEndpointsAbsent(ctx context.Context, _ string, _ map[string]db.ISOComponent) error {
	return r.lifecycle.compose.VerifyDefaultNetworkAbsent(ctx)
}
func (r *isoConcreteRetirementSteps) VerifyDNetRecordsAbsent(_ context.Context, _ string, retired map[string]db.ISOComponent) error {
	dv, err := r.lifecycle.si.s.cfg.DB.Get()
	if err != nil {
		return err
	}
	retiredAddresses := map[netip.Addr]bool{}
	for _, component := range retired {
		retiredAddresses[component.Address] = true
	}
	return verifyDNetAddressesAbsent(dv.AsStruct().DockerNetworks, retiredAddresses, "retired address")
}
func (r *isoConcreteRetirementSteps) Reserve(ctx context.Context, service string) error {
	allocation, err := r.lifecycle.si.s.reserveISOAllocation(ctx, service, isoReservationRequest{
		Kind: iso.PayloadCompose, Modes: slices.Clone(r.lifecycle.record.ISO.DesiredModes), Components: slices.Clone(r.lifecycle.base.Components),
	})
	r.lifecycle.allocation = allocation
	return err
}
func (r *isoConcreteRetirementSteps) Quarantine(_ context.Context, service string, cause error) error {
	return r.lifecycle.si.s.markISOState(service, string(iso.StateQuarantined), cause)
}

func installDockerComposeServiceDefinition(si *Installer, s *db.Service) error {
	si.suspendUI()
	if s.ISO != nil {
		return si.installISOComposeServiceDefinition(s)
	}
	if _, err := svc.DockerCmd(); err != nil {
		return err
	}
	service, err := si.newDockerComposeService(s)
	if err != nil {
		return fmt.Errorf("failed to create service: %v", err)
	}
	if err := service.InstallWithPull(si.icfg.Pull); err != nil {
		return fmt.Errorf("failed to install service: %v", err)
	}
	return nil
}

func (si *Installer) installISOComposeServiceDefinition(record *db.Service) error {
	if _, err := svc.DockerCmd(); err != nil {
		return err
	}
	compose, err := si.newDockerComposeService(record)
	if err != nil {
		return err
	}
	lifecycle := &isoComposeLifecycle{si: si, record: record.Clone(), compose: compose}
	if err := runISOInstallPhases(context.Background(), lifecycle); err != nil {
		return err
	}
	if err := lifecycle.compose.InstallDefinition(); err != nil {
		return errors.Join(err, lifecycle.Quarantine(context.Background(), err))
	}
	return si.s.markISOStoppedIfAllocated(record.Name)
}

func runISOInstallPhases(ctx context.Context, lifecycle *isoComposeLifecycle) error {
	phases := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "resolve-base", run: lifecycle.ResolveBase},
		{name: "admit-base", run: lifecycle.AdmitBase},
		{name: "reserve", run: lifecycle.Reserve},
		{name: "render-overlay", run: lifecycle.RenderOverlay},
		{name: "resolve-merged", run: lifecycle.ResolveMerged},
		{name: "admit-merged", run: lifecycle.AdmitMerged},
		{name: "install-dns", run: lifecycle.InstallDNS},
		{name: "ensure-policy", run: lifecycle.EnsurePolicy},
		{name: "verify-policy", run: lifecycle.VerifyPolicy},
		{name: "ensure-topology", run: lifecycle.EnsureTopology},
		{name: "verify-topology", run: lifecycle.VerifyTopology},
		{name: "install-tailscale", run: func(ctx context.Context) error { return lifecycle.InstallTailscale(ctx, nil) }},
	}
	for _, phase := range phases {
		if err := runISOInstallPhase(ctx, phase.name, phase.run); err != nil {
			return errors.Join(err, lifecycle.Quarantine(ctx, err))
		}
	}
	return nil
}

func (si *Installer) suspendUI() {
	if si.icfg.UI != nil {
		si.icfg.UI.Suspend()
	}
}

func (si *Installer) newDockerComposeService(s *db.Service) (*svc.DockerComposeService, error) {
	serviceRoot := si.s.serviceRootFromView(s.View())
	service, err := svc.NewDockerComposeService(si.s.cfg.DB, s.View(), serviceDataDirForRoot(serviceRoot), serviceRunDirForRoot(serviceRoot))
	if err != nil {
		return nil, err
	}
	si.configureDockerComposeCommands(service)
	return service, nil
}

func (si *Installer) configureDockerComposeCommands(service *svc.DockerComposeService) {
	service.NewCmd = si.NewCmd
	service.NewCmdContext = si.newCommandContext
}

func (si *Installer) newCommandContext(_ context.Context, name string, args ...string) *exec.Cmd {
	return si.NewCmd(name, args...)
}

func (si *Installer) publishInstallEvent(s *db.Service) {
	si.s.PublishEvent(Event{
		Type:        installEventType(s.LatestGeneration),
		ServiceName: s.Name,
		Data:        EventData{s.View()},
	})
}

func installEventType(latestGeneration int) EventType {
	if latestGeneration == 1 {
		return EventTypeServiceCreated
	}
	return EventTypeServiceConfigChanged
}

func asJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("failed to marshal: %v", err)
	}
	return string(b)
}
