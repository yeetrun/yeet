// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/db"
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

	icfg InstallerCfg
	s    *Server
}

func unassignedIP(dv db.DataView) (netip.Addr, error) {
	isAssignedIP := func(ip netip.Addr) bool {
		for _, s := range dv.AsStruct().Services {
			if s.SvcNetwork != nil && s.SvcNetwork.IPv4 == ip {
				return true
			}
		}
		return false
	}
	ip := netip.MustParseAddr("192.168.100.3")
	pfx := netip.MustParsePrefix("192.168.100.0/24")
	max := netip.MustParseAddr("192.168.100.253")
	for isAssignedIP(ip) && ip.Less(max) {
		ip = ip.Next()
	}
	if !pfx.Contains(ip) || ip.Compare(max) > 0 {
		return netip.Addr{}, fmt.Errorf("no available IP address")
	}
	return ip, nil
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
	d, s, err := si.mutateService(func(d *db.Data, s *db.Service) error {
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
	_, _, err := si.mutateService(func(d *db.Data, s *db.Service) error {
		pruneServiceArtifacts(s, knownBins)
		return nil
	})
	if err != nil {
		log.Printf("failed to mutate service: %v", err)
		return
	}

	si.pruneInstallDirectories(knownBins)
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

func (si *Installer) pruneInstallDirectories(knownFiles set.Set[string]) {
	for _, dir := range []string{
		si.s.serviceBinDir(si.icfg.ServiceName),
		si.s.serviceEnvDir(si.icfg.ServiceName),
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

	d, s, err := si.commitGen(gen)
	if err != nil {
		return fmt.Errorf("failed to commit gen: %v", err)
	}

	si.prune()

	return si.doInstall(d, s)
}

// Install installs the service.
func (si *Installer) Install() error {
	return si.InstallGen(0)
}

func (si *Installer) doInstall(_ *db.Data, s *db.Service) error {
	if err := validateInstallRequest(si.icfg.Pull, s.ServiceType); err != nil {
		return err
	}
	if err := si.runInstallPhase(s); err != nil {
		return err
	}
	si.publishInstallEvent(s)
	return nil
}

func validateInstallRequest(pull bool, serviceType db.ServiceType) error {
	if pull && serviceType != db.ServiceTypeDockerCompose {
		return fmt.Errorf("--pull is only valid for docker compose payloads")
	}
	return nil
}

type installPhase func(*Installer, *db.Service) error

func (si *Installer) runInstallPhase(s *db.Service) error {
	phase, err := installPhaseForServiceType(s.ServiceType)
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

func newSystemdInstallService(si *Installer, s *db.Service) (*svc.SystemdService, error) {
	service, err := svc.NewSystemdService(si.s.cfg.DB, s.View(), si.s.serviceRunDir(si.icfg.ServiceName))
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

func (si *Installer) suspendUI() {
	if si.icfg.UI != nil {
		si.icfg.UI.Suspend()
	}
}

func (si *Installer) newDockerComposeService(s *db.Service) (*svc.DockerComposeService, error) {
	service, err := svc.NewDockerComposeService(si.s.cfg.DB, s.View(), si.s.serviceDataDir(s.Name), si.s.serviceRunDir(s.Name))
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
