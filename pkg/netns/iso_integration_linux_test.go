// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && integration

package netns

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

const isoIntegrationTimeout = 3 * time.Second

type isoIntegrationBackend struct {
	backend FirewallBackend
	skip    string
}

type isoIntegrationLab struct {
	t        *testing.T
	backend  FirewallBackend
	suffix   string
	helper   string
	upstream string
	projects map[string]*isoIntegrationProject
}

type isoIntegrationProject struct {
	spec      ISOTopologySpec
	endpoints map[string]string
}

func TestISOPacketPolicy(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("ISO integration test requires root")
	}
	if os.Getenv("YEET_ISO_INTEGRATION") != "1" {
		t.Skip("set YEET_ISO_INTEGRATION=1")
	}
	requireISOIntegrationRootNamespace(t)
	requireISOIntegrationCommand(t, "ip")
	requireISOIntegrationCommand(t, "sysctl")
	helper := buildISOIntegrationEndpoint(t)

	for _, candidate := range availableISOIntegrationBackends() {
		t.Run(string(candidate.backend), func(t *testing.T) {
			if selected := os.Getenv("YEET_ISO_INTEGRATION_BACKEND"); selected != "" && selected != string(candidate.backend) {
				t.Skip("backend not selected")
			}
			if candidate.skip != "" {
				t.Skip(candidate.skip)
			}
			lab := newISOIntegrationLab(t, candidate.backend, helper)
			lab.startRootDNS(map[string]string{"public.test.": "1.1.1.10"})
			lab.startUpstreamTCP("1.1.1.10", 18080)
			lab.startUpstreamTCP("1.1.1.10", 853)
			lab.startUpstreamDNS(map[string]string{"bypass.test.": "9.9.9.9"})

			projectA := lab.projectSpec("a", "172.30.0.0/30", "172.30.128.0/27", []string{"172.30.128.2", "172.30.128.3"})
			projectB := lab.projectSpec("b", "172.30.0.4/30", "172.30.128.32/27", []string{"172.30.128.34"})
			lab.ensurePolicy(projectA, projectB)
			lab.ensureProject("a", projectA)
			lab.ensureProject("b", projectB)
			lab.startEndpointTCP("a", "172.30.128.2", 18080)
			lab.startEndpointTCP("a", "172.30.128.3", 18080)
			lab.startEndpointTCP("b", "172.30.128.34", 18080)
			lab.startRootPrivateTargets(18080)

			lab.assertHostConnects("172.30.128.2", 18080)
			lab.assertEndpointConnects("a", "172.30.128.2", "1.1.1.10", 18080, "1.1.1.1")
			lab.assertEndpointResolves("a", "172.30.128.2", "172.30.128.1:53", "public.test.", "1.1.1.10")
			lab.assertEndpointDNSRejected("a", "172.30.128.2", "172.30.128.1:53", "private.test.", "10.0.0.10")
			lab.assertEndpointConnects("a", "172.30.128.2", "172.30.128.3", 18080, "")
			lab.assertEndpointRejected("a", "172.30.128.2", "172.30.0.1", 18080)
			lab.assertEndpointRejected("a", "172.30.128.2", "192.168.100.1", 18080)
			lab.assertEndpointRejected("a", "172.30.128.2", "169.254.169.254", 18080)
			lab.assertEndpointRejected("a", "172.30.128.2", "100.100.100.100", 18080)
			lab.assertEndpointRejected("a", "172.30.128.2", "172.30.128.34", 18080)
			lab.assertDirectDNSCannotBypass("a", "172.30.128.2")
			lab.assertEndpointRejected("a", "172.30.128.2", "1.1.1.10", 853)
			lab.assertSpoofedSourceDropped("a", "172.30.128.2", "172.30.128.34")
			lab.assertIPv6Unavailable("a", "172.30.128.2")
		})
	}
}

func requireISOIntegrationRootNamespace(t *testing.T) {
	t.Helper()
	if os.Getenv("YEET_ISO_INTEGRATION_ROOTNS") != "1" {
		t.Fatal("run ISO integration tests through tools/test-iso-network.sh so policy is installed only in a disposable root network namespace")
	}
	self, selfErr := os.Readlink("/proc/self/ns/net")
	init, initErr := os.Readlink("/proc/1/ns/net")
	if selfErr != nil || initErr != nil {
		t.Fatalf("inspect network namespace identity: self=%v init=%v", selfErr, initErr)
	}
	if self == init {
		t.Fatal("ISO integration test refused to modify the initial network namespace")
	}
}

func availableISOIntegrationBackends() []isoIntegrationBackend {
	backends := []FirewallBackend{BackendNFT, BackendIPTablesNFT, BackendIPTablesLegacy}
	out := make([]isoIntegrationBackend, 0, len(backends))
	for _, backend := range backends {
		candidate := isoIntegrationBackend{backend: backend}
		for _, command := range isoIntegrationBackendCommands(backend) {
			if _, err := exec.LookPath(command); err != nil {
				candidate.skip = fmt.Sprintf("%s unavailable: %s not found", backend, command)
				break
			}
		}
		if candidate.skip == "" {
			probe := isoIntegrationBackendCommands(backend)[0]
			if output, err := exec.Command(probe, "--version").CombinedOutput(); err != nil {
				candidate.skip = fmt.Sprintf("%s unavailable: %s --version: %v: %s", backend, probe, err, strings.TrimSpace(string(output)))
			}
		}
		out = append(out, candidate)
	}
	return out
}

func isoIntegrationBackendCommands(backend FirewallBackend) []string {
	if backend == BackendNFT {
		return []string{"nft"}
	}
	restore4, save4, _ := isoIPTablesTools(backend, false)
	restore6, save6, _ := isoIPTablesTools(backend, true)
	bin, _ := iptablesBinary(backend)
	return []string{bin, strings.Replace(bin, "iptables", "ip6tables", 1), restore4, save4, restore6, save6, "ipset"}
}

func buildISOIntegrationEndpoint(t *testing.T) string {
	t.Helper()
	if helper := os.Getenv("YEET_ISO_ENDPOINT_HELPER"); helper != "" {
		if info, err := os.Stat(helper); err != nil || info.IsDir() {
			t.Fatalf("YEET_ISO_ENDPOINT_HELPER %q is not an executable file", helper)
		}
		return helper
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate integration test source")
	}
	out := filepath.Join(t.TempDir(), "iso-endpoint")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/iso-endpoint")
	cmd.Dir = filepath.Dir(filename)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build ISO endpoint helper: %v\n%s", err, output)
	}
	return out
}

func newISOIntegrationLab(t *testing.T, backend FirewallBackend, helper string) *isoIntegrationLab {
	t.Helper()
	lab := &isoIntegrationLab{
		t: t, backend: backend, suffix: randomISOIntegrationSuffix(t), helper: helper,
		projects: map[string]*isoIntegrationProject{},
	}
	lab.upstream = "yiu-" + lab.suffix
	lab.mustRun("ip", "link", "set", "lo", "up")
	lab.mustRun("sysctl", "-w", "net.ipv4.ip_forward=1")
	lab.mustRun("ip", "netns", "add", lab.upstream)
	t.Cleanup(func() { lab.runIgnoringError("ip", "netns", "delete", lab.upstream) })
	rootIf := "yu" + lab.suffix
	peerIf := "yp" + lab.suffix
	lab.mustRun("ip", "link", "add", rootIf, "type", "veth", "peer", "name", peerIf)
	lab.mustRun("ip", "link", "set", peerIf, "netns", lab.upstream)
	lab.mustRun("ip", "address", "add", "1.1.1.1/24", "dev", rootIf)
	lab.mustRun("ip", "link", "set", rootIf, "up")
	lab.mustRunIn(lab.upstream, "ip", "link", "set", "lo", "up")
	lab.mustRunIn(lab.upstream, "ip", "address", "add", "1.1.1.10/24", "dev", peerIf)
	lab.mustRunIn(lab.upstream, "ip", "link", "set", peerIf, "up")
	lab.mustRunIn(lab.upstream, "ip", "route", "add", "default", "via", "1.1.1.1", "dev", peerIf)
	return lab
}

func (l *isoIntegrationLab) projectSpec(label, linkRaw, projectRaw string, endpointIPs []string) ISOTopologySpec {
	l.t.Helper()
	link := netip.MustParsePrefix(linkRaw)
	project := netip.MustParsePrefix(projectRaw)
	components := make(map[string]db.ISOComponent, len(endpointIPs))
	for index, raw := range endpointIPs {
		components[fmt.Sprintf("endpoint-%d", index)] = db.ISOComponent{Address: netip.MustParseAddr(raw), State: string(iso.StateReady)}
	}
	allocation := db.ISOAllocation{
		Kind: string(iso.PayloadCompose), DesiredModes: []string{"iso"}, State: string(iso.StateReady),
		Link: link, HostIP: link.Addr().Next(), PeerIP: link.Addr().Next().Next(),
		Project: project, Gateway: project.Addr().Next(), Components: components,
		Interface: "yi" + l.suffix + label, PeerInterface: "yo" + l.suffix + label,
		NetNS: "yin-" + l.suffix + "-" + label, Bridge: "br0",
		AllocatorVersion: iso.AllocatorVersion, PolicyVersion: iso.PolicyVersion,
	}
	return ISOTopologySpec{Backend: l.backend, Pool: netip.MustParsePrefix("172.30.0.0/16"), Allocation: allocation}
}

func (l *isoIntegrationLab) ensurePolicy(specs ...ISOTopologySpec) {
	l.t.Helper()
	endpoints := make([]ISOEndpoint, 0, len(specs))
	for _, spec := range specs {
		a := spec.Allocation
		endpoints = append(endpoints, ISOEndpoint{Interface: a.Interface, Link: a.Link, PeerIP: a.PeerIP, Project: a.Project})
	}
	rules, err := RenderISOPolicy(l.backend, ISOPolicySpec{Pool: netip.MustParsePrefix("172.30.0.0/16"), DNSPort: 5353, Endpoints: endpoints})
	if err != nil {
		l.t.Fatal(err)
	}
	if err := EnsureISOPolicy(context.Background(), rules); err != nil {
		live, liveErr := readLiveISOPolicy(context.Background(), l.backend)
		l.t.Fatalf("ensure %s ISO policy: %v\nread live: %v\nwant IPv4:\n%s\nlive IPv4:\n%s\nwant IPv6:\n%s\nlive IPv6:\n%s\nwant ipset:\n%s\nlive ipset:\n%s",
			l.backend, err, liveErr,
			canonicalISOFirewallText(l.backend, rules.IPv4), canonicalISOFirewallText(l.backend, live.IPv4),
			canonicalISOFirewallText(l.backend, rules.IPv6), canonicalISOFirewallText(l.backend, live.IPv6),
			canonicalISOIPSetText(rules.IPSet), canonicalISOIPSetText(live.IPSet))
	}
}

func (l *isoIntegrationLab) ensureProject(label string, spec ISOTopologySpec) {
	l.t.Helper()
	if err := EnsureISOTopology(context.Background(), spec); err != nil {
		if l.backend != BackendNFT {
			want4, want6 := renderIPTablesISORouterPolicy(spec, spec.Allocation.Bridge)
			_, save4, _ := isoIPTablesTools(l.backend, false)
			_, save6, _ := isoIPTablesTools(l.backend, true)
			live4, live4Err := l.commandOutput("ip", "netns", "exec", spec.Allocation.NetNS, save4)
			live6, live6Err := l.commandOutput("ip", "netns", "exec", spec.Allocation.NetNS, save6)
			l.t.Fatalf("ensure %s project %s: %v\nIPv4 read: %v\nwant:\n%s\nlive:\n%s\nIPv6 read: %v\nwant:\n%s\nlive:\n%s",
				l.backend, label, err, live4Err, canonicalISORouterIPTables(want4), canonicalISORouterIPTables(live4),
				live6Err, canonicalISORouterIPTables(want6), canonicalISORouterIPTables(live6))
		}
		l.t.Fatalf("ensure %s project %s: %v", l.backend, label, err)
	}
	l.t.Cleanup(func() {
		if err := RemoveISOTopology(context.Background(), spec); err != nil {
			l.t.Errorf("remove project %s: %v", label, err)
		}
	})
	l.mustRunIn(spec.Allocation.NetNS, "ip", "link", "add", spec.Allocation.Bridge, "type", "bridge")
	l.mustRunIn(spec.Allocation.NetNS, "ip", "address", "add", netip.PrefixFrom(spec.Allocation.Gateway, spec.Allocation.Project.Bits()).String(), "dev", spec.Allocation.Bridge)
	l.mustRunIn(spec.Allocation.NetNS, "ip", "link", "set", spec.Allocation.Bridge, "up")

	project := &isoIntegrationProject{spec: spec, endpoints: map[string]string{}}
	names := make([]string, 0, len(spec.Allocation.Components))
	for name := range spec.Allocation.Components {
		names = append(names, name)
	}
	for index, name := range names {
		address := spec.Allocation.Components[name].Address
		namespace := fmt.Sprintf("yie-%s-%s%d", l.suffix, label, index)
		project.endpoints[address.String()] = namespace
		l.mustRun("ip", "netns", "add", namespace)
		l.t.Cleanup(func() { l.runIgnoringError("ip", "netns", "delete", namespace) })
		routerIf := fmt.Sprintf("ve%d", index)
		endpointIf := fmt.Sprintf("ep%d", index)
		l.mustRunIn(spec.Allocation.NetNS, "ip", "link", "add", routerIf, "type", "veth", "peer", "name", endpointIf)
		l.mustRunIn(spec.Allocation.NetNS, "ip", "link", "set", endpointIf, "netns", namespace)
		l.mustRunIn(spec.Allocation.NetNS, "ip", "link", "set", routerIf, "master", spec.Allocation.Bridge)
		l.mustRunIn(spec.Allocation.NetNS, "ip", "link", "set", routerIf, "up")
		l.mustRunIn(namespace, "ip", "link", "set", "lo", "up")
		l.mustRunIn(namespace, "sysctl", "-w", "net.ipv6.conf.all.disable_ipv6=1")
		l.mustRunIn(namespace, "sysctl", "-w", "net.ipv6.conf.default.disable_ipv6=1")
		l.mustRunIn(namespace, "ip", "address", "add", netip.PrefixFrom(address, spec.Allocation.Project.Bits()).String(), "dev", endpointIf)
		l.mustRunIn(namespace, "ip", "link", "set", endpointIf, "up")
		l.mustRunIn(namespace, "ip", "route", "add", "default", "via", spec.Allocation.Gateway.String(), "dev", endpointIf)
	}
	l.projects[label] = project
}

func (l *isoIntegrationLab) startRootDNS(records map[string]string) {
	l.startDNS("", "0.0.0.0:5353", "127.0.0.1:5353", records)
}

func (l *isoIntegrationLab) startUpstreamDNS(records map[string]string) {
	l.startDNS(l.upstream, "1.1.1.10:53", "1.1.1.10:53", records)
}

func (l *isoIntegrationLab) startDNS(namespace, listenAddress, probeAddress string, records map[string]string) {
	l.t.Helper()
	args := []string{"listen", "--address", listenAddress}
	var probeName, probeIP string
	for name, ip := range records {
		args = append(args, "--dns-record", name+"="+ip)
		if probeName == "" {
			probeName, probeIP = name, ip
		}
	}
	process := l.startProcess(namespace, args...)
	l.waitReady(process, func() error {
		_, err := l.runHelper(namespace, "dns", "--server", probeAddress, "--name", probeName, "--want", probeIP)
		return err
	})
}

func (l *isoIntegrationLab) startUpstreamTCP(ip string, port int) {
	l.startTCP(l.upstream, fmt.Sprintf("%s:%d", ip, port))
}

func (l *isoIntegrationLab) startEndpointTCP(project, ip string, port int) {
	namespace := l.endpointNamespace(project, ip)
	l.startTCP(namespace, fmt.Sprintf("%s:%d", ip, port))
}

func (l *isoIntegrationLab) startTCP(namespace, address string) {
	l.t.Helper()
	process := l.startProcess(namespace, "listen", "--address", address)
	l.waitReady(process, func() error {
		_, err := l.runHelper(namespace, "connect", "--address", address)
		return err
	})
}

func (l *isoIntegrationLab) startRootPrivateTargets(port int) {
	l.t.Helper()
	for _, ip := range []string{"192.168.100.1", "169.254.169.254", "100.100.100.100"} {
		l.mustRun("ip", "address", "add", ip+"/32", "dev", "lo")
		ip := ip
		l.t.Cleanup(func() { l.runIgnoringError("ip", "address", "delete", ip+"/32", "dev", "lo") })
		l.startTCP("", fmt.Sprintf("%s:%d", ip, port))
	}
	l.startTCP("", fmt.Sprintf("%s:%d", l.projects["a"].spec.Allocation.HostIP, port))
}

func (l *isoIntegrationLab) assertHostConnects(ip string, port int) {
	l.t.Helper()
	if output, err := l.runHelper("", "connect", "--address", fmt.Sprintf("%s:%d", ip, port)); err != nil {
		l.t.Fatalf("host cannot connect to ISO endpoint %s: %v\n%s", ip, err, output)
	}
}

func (l *isoIntegrationLab) assertEndpointConnects(project, source, destination string, port int, wantRemote string) {
	l.t.Helper()
	args := []string{"connect", "--address", fmt.Sprintf("%s:%d", destination, port), "--source", source}
	if wantRemote != "" {
		args = append(args, "--want-remote", wantRemote)
	}
	if output, err := l.runHelper(l.endpointNamespace(project, source), args...); err != nil {
		l.t.Fatalf("endpoint %s cannot connect to %s: %v\n%s", source, destination, err, output)
	}
}

func (l *isoIntegrationLab) assertEndpointRejected(project, source, destination string, port int) {
	l.t.Helper()
	if output, err := l.runHelper(l.endpointNamespace(project, source), "connect", "--address", fmt.Sprintf("%s:%d", destination, port), "--source", source); err == nil {
		l.t.Fatalf("endpoint %s unexpectedly connected to %s:%d\n%s", source, destination, port, output)
	}
}

func (l *isoIntegrationLab) assertEndpointResolves(project, source, server, name, want string) {
	l.t.Helper()
	if output, err := l.runHelper(l.endpointNamespace(project, source), "dns", "--server", server, "--name", name, "--want", want); err != nil {
		l.t.Fatalf("endpoint %s cannot resolve %s through %s: %v\n%s", source, name, server, err, output)
	}
}

func (l *isoIntegrationLab) assertEndpointDNSRejected(project, source, server, name, forbidden string) {
	l.t.Helper()
	if output, err := l.runHelper(l.endpointNamespace(project, source), "dns", "--server", server, "--name", name, "--want", forbidden); err == nil {
		l.t.Fatalf("endpoint %s unexpectedly resolved filtered name %s\n%s", source, name, output)
	}
}

func (l *isoIntegrationLab) assertDirectDNSCannotBypass(project, source string) {
	l.t.Helper()
	namespace := l.endpointNamespace(project, source)
	if output, err := l.runHelper(namespace, "dns", "--server", "1.1.1.10:53", "--name", "public.test.", "--want", "1.1.1.10"); err != nil {
		l.t.Fatalf("direct DNS was not redirected to the enforced resolver: %v\n%s", err, output)
	}
	if output, err := l.runHelper(namespace, "dns", "--server", "1.1.1.10:53", "--name", "bypass.test.", "--want", "9.9.9.9"); err == nil {
		l.t.Fatalf("direct DNS bypass reached the upstream resolver\n%s", output)
	}
}

func (l *isoIntegrationLab) assertSpoofedSourceDropped(project, source, spoof string) {
	l.t.Helper()
	namespace := l.endpointNamespace(project, source)
	endpointIf := l.endpointInterface(project, source)
	l.mustRunIn(namespace, "ip", "address", "add", spoof+"/32", "dev", endpointIf)
	if output, err := l.runHelper(namespace, "spoof", "--address", "1.1.1.10:18080", "--source", spoof); err == nil {
		l.t.Fatalf("spoofed source %s reached public endpoint\n%s", spoof, output)
	}
}

func (l *isoIntegrationLab) assertIPv6Unavailable(project, source string) {
	l.t.Helper()
	namespace := l.endpointNamespace(project, source)
	output := l.mustOutput("ip", "netns", "exec", namespace, "ip", "-6", "route", "show", "default")
	if strings.TrimSpace(output) != "" {
		l.t.Fatalf("endpoint %s has an IPv6 default route: %s", source, output)
	}
	if output, err := l.runHelper(namespace, "connect", "--address", "[2606:4700:4700::1111]:18080"); err == nil {
		l.t.Fatalf("endpoint %s unexpectedly connected over IPv6\n%s", source, output)
	}
}

func (l *isoIntegrationLab) endpointNamespace(project, ip string) string {
	l.t.Helper()
	p := l.projects[project]
	if p == nil || p.endpoints[ip] == "" {
		l.t.Fatalf("unknown endpoint %s/%s", project, ip)
	}
	return p.endpoints[ip]
}

func (l *isoIntegrationLab) endpointInterface(project, ip string) string {
	l.t.Helper()
	p := l.projects[project]
	namespace := l.endpointNamespace(project, ip)
	for index, component := range p.spec.Allocation.Components {
		if component.Address.String() == ip {
			_ = index
			output := l.mustOutput("ip", "netns", "exec", namespace, "ip", "-o", "link", "show")
			for _, line := range strings.Split(output, "\n") {
				fields := strings.Fields(line)
				if len(fields) > 1 && strings.HasPrefix(strings.TrimSuffix(fields[1], ":"), "ep") {
					return strings.Split(strings.TrimSuffix(fields[1], ":"), "@")[0]
				}
			}
		}
	}
	l.t.Fatalf("find endpoint interface for %s/%s", project, ip)
	return ""
}

func (l *isoIntegrationLab) startProcess(namespace string, args ...string) *exec.Cmd {
	l.t.Helper()
	command := append([]string(nil), args...)
	if namespace != "" {
		command = append([]string{"netns", "exec", namespace, l.helper}, command...)
		command = append([]string{"ip"}, command...)
	} else {
		command = append([]string{l.helper}, command...)
	}
	cmd := exec.Command(command[0], command[1:]...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		l.t.Fatalf("start %s: %v", strings.Join(command, " "), err)
	}
	l.t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	return cmd
}

func (l *isoIntegrationLab) waitReady(process *exec.Cmd, probe func() error) {
	l.t.Helper()
	deadline := time.Now().Add(isoIntegrationTimeout)
	for time.Now().Before(deadline) {
		if probe() == nil {
			return
		}
		if process.ProcessState != nil && process.ProcessState.Exited() {
			l.t.Fatal("endpoint helper exited before readiness")
		}
		time.Sleep(25 * time.Millisecond)
	}
	l.t.Fatal("timed out waiting for endpoint helper readiness")
}

func (l *isoIntegrationLab) runHelper(namespace string, args ...string) (string, error) {
	command := append([]string(nil), args...)
	if namespace != "" {
		command = append([]string{"netns", "exec", namespace, l.helper}, command...)
		command = append([]string{"ip"}, command...)
	} else {
		command = append([]string{l.helper}, command...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), isoIntegrationTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput()
	return string(output), err
}

func (l *isoIntegrationLab) mustRunIn(namespace, name string, args ...string) {
	l.t.Helper()
	command := append([]string{"netns", "exec", namespace, name}, args...)
	l.mustRun("ip", command...)
}

func (l *isoIntegrationLab) mustRun(name string, args ...string) {
	l.t.Helper()
	if output, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		l.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
}

func (l *isoIntegrationLab) mustOutput(name string, args ...string) string {
	l.t.Helper()
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		l.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return string(output)
}

func (l *isoIntegrationLab) commandOutput(name string, args ...string) (string, error) {
	output, err := exec.Command(name, args...).CombinedOutput()
	return string(output), err
}

func (l *isoIntegrationLab) runIgnoringError(name string, args ...string) {
	_ = exec.Command(name, args...).Run()
}

func requireISOIntegrationCommand(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("ISO integration test requires %s", name)
	}
}

func randomISOIntegrationSuffix(t *testing.T) string {
	t.Helper()
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw[:])
}
