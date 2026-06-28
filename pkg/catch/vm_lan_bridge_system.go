// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	vmLANBridgeNetplanOverlayMode = 0600
	vmLANBridgeValidationRetries  = 12
	vmLANBridgeValidationDelay    = 500 * time.Millisecond
)

var vmLANBridgeNetplanGlobs = []string{
	"/lib/netplan/*.yaml",
	"/etc/netplan/*.yaml",
	"/run/netplan/*.yaml",
}

var (
	vmLANBridgeCommandOutputFn = defaultVMLANBridgeCommandOutput
	vmLANBridgeRunCommandFn    = defaultVMLANBridgeRunCommand
	vmLANBridgeNetplanGlobFn   = filepath.Glob
	vmLANBridgeReadFileFn      = os.ReadFile
	vmLANBridgeRemoveFileFn    = os.Remove
	vmLANBridgeSleepFn         = time.Sleep
)

type vmLANNetplanFile struct {
	path    string
	content []byte
	doc     netplanDocument
}

type vmLANNetplanScanResult struct {
	file          vmLANNetplanFile
	err           error
	definesParent bool
}

type systemVMLANBridgePrepareRunner struct {
	netplanSourcePath string
	planFn            func() (vmLANBridgePlan, error)
}

func newSystemVMLANBridgePrepareRunner(root string) vmLANBridgePrepareRunner {
	return &systemVMLANBridgePrepareRunner{planFn: planSystemVMLANBridge}
}

func planSystemVMLANBridge() (vmLANBridgePlan, error) {
	state, err := discoverVMLANHostState()
	if err != nil {
		return vmLANBridgePlan{}, err
	}
	plan, err := planHostLANBridge(state)
	if err != nil {
		return plan, err
	}
	if !plan.NeedsPrepare {
		return plan, nil
	}
	if _, err := readVMLANNetplanForParent(plan.Parent); err != nil {
		return plan, err
	}
	plan.Renderer = vmLANRenderer{Name: "netplan-networkd", Supported: true}
	return plan, nil
}

func discoverVMLANHostState() (fakeVMLANHostState, error) {
	links, err := discoverVMLANLinks()
	if err != nil {
		return fakeVMLANHostState{}, err
	}
	routes, err := discoverVMLANRoutes()
	if err != nil {
		return fakeVMLANHostState{}, err
	}
	addrs, err := discoverVMLANAddresses()
	if err != nil {
		return fakeVMLANHostState{}, err
	}
	return fakeVMLANHostState{
		links:    links,
		routes:   routes,
		addrs:    addrs,
		renderer: discoverVMLANNetplanRenderer(),
	}, nil
}

func discoverVMLANLinks() ([]vmLANLink, error) {
	raw, err := vmLANBridgeCommandOutputFn("ip", "-json", "link", "show")
	if err != nil {
		return nil, fmt.Errorf("discover VM LAN links: %w", err)
	}
	links, err := parseVMLANLinks(raw)
	if err != nil {
		return nil, fmt.Errorf("parse VM LAN links: %w", err)
	}
	return links, nil
}

func discoverVMLANRoutes() ([]vmLANRoute, error) {
	raw, err := vmLANBridgeCommandOutputFn("ip", "-json", "route", "show", "default")
	if err != nil {
		return nil, fmt.Errorf("discover VM LAN default routes: %w", err)
	}
	routes, err := parseVMLANRoutes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse VM LAN default routes: %w", err)
	}
	return routes, nil
}

func discoverVMLANAddresses() ([]vmLANAddress, error) {
	raw, err := vmLANBridgeCommandOutputFn("ip", "-json", "address", "show")
	if err != nil {
		return nil, fmt.Errorf("discover VM LAN addresses: %w", err)
	}
	addrs, err := parseVMLANAddresses(raw)
	if err != nil {
		return nil, fmt.Errorf("parse VM LAN addresses: %w", err)
	}
	return addrs, nil
}

func parseVMLANLinks(raw []byte) ([]vmLANLink, error) {
	var entries []struct {
		IfName    string `json:"ifname"`
		OperState string `json:"operstate"`
		Master    string `json:"master"`
		LinkType  string `json:"link_type"`
		Address   string `json:"address"`
		LinkInfo  struct {
			InfoKind string `json:"info_kind"`
		} `json:"linkinfo"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	links := make([]vmLANLink, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.IfName)
		if name == "" {
			continue
		}
		kind := strings.TrimSpace(entry.LinkInfo.InfoKind)
		if kind == "" {
			kind = strings.TrimSpace(entry.LinkType)
		}
		links = append(links, vmLANLink{
			Name:        name,
			Kind:        strings.ToLower(kind),
			OperState:   strings.ToLower(strings.TrimSpace(entry.OperState)),
			Master:      strings.TrimSpace(entry.Master),
			HasHardware: isVMLANHardwareAddress(entry.Address),
		})
	}
	return links, nil
}

func parseVMLANRoutes(raw []byte) ([]vmLANRoute, error) {
	var entries []struct {
		Dst     string `json:"dst"`
		Dev     string `json:"dev"`
		Gateway string `json:"gateway"`
		Metric  int    `json:"metric"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	routes := make([]vmLANRoute, 0, len(entries))
	for _, entry := range entries {
		iface := strings.TrimSpace(entry.Dev)
		if iface == "" {
			continue
		}
		dst := strings.TrimSpace(entry.Dst)
		routes = append(routes, vmLANRoute{
			Default: dst == "" || dst == "default",
			Iface:   iface,
			Gateway: strings.TrimSpace(entry.Gateway),
			Metric:  entry.Metric,
		})
	}
	return routes, nil
}

func parseVMLANAddresses(raw []byte) ([]vmLANAddress, error) {
	var entries []struct {
		IfName   string `json:"ifname"`
		AddrInfo []struct {
			Family    string `json:"family"`
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
			Scope     string `json:"scope"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	addrs := []vmLANAddress{}
	for _, entry := range entries {
		iface := strings.TrimSpace(entry.IfName)
		if iface == "" {
			continue
		}
		for _, addr := range entry.AddrInfo {
			if strings.TrimSpace(addr.Family) != "inet" {
				continue
			}
			if scope := strings.TrimSpace(addr.Scope); scope != "" && scope != "global" {
				continue
			}
			local := strings.TrimSpace(addr.Local)
			if local == "" || addr.PrefixLen <= 0 {
				continue
			}
			addrs = append(addrs, vmLANAddress{
				Iface:  iface,
				Prefix: local + "/" + strconv.Itoa(addr.PrefixLen),
				Scope:  "global",
			})
		}
	}
	return addrs, nil
}

func isVMLANHardwareAddress(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	_, err := net.ParseMAC(value)
	return err == nil
}

func discoverVMLANNetplanRenderer() vmLANRenderer {
	files, errs := readSupportedVMLANNetplanFiles()
	if len(files) > 0 {
		return vmLANRenderer{Name: "netplan-networkd", Supported: true}
	}
	return vmLANRenderer{Name: "netplan", Supported: false, Reason: vmLANNetplanUnsupportedReason(errs)}
}

func readVMLANNetplanForParent(parent string) (vmLANNetplanFile, error) {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return vmLANNetplanFile{}, fmt.Errorf("VM LAN bridge parent interface is required")
	}
	results, errs := scanVMLANNetplanFiles(parent)
	matches := []vmLANNetplanFile{}
	unsupported := []string{}
	for _, result := range results {
		if !result.definesParent {
			continue
		}
		if result.err != nil {
			unsupported = append(unsupported, fmt.Sprintf("%s: %v", result.file.path, result.err))
			continue
		}
		matches = append(matches, result.file)
	}
	if len(unsupported) > 0 {
		return vmLANNetplanFile{}, fmt.Errorf("unsupported netplan config defines network.ethernets.%s: %s", parent, strings.Join(unsupported, "; "))
	}
	if len(matches) > 1 {
		return vmLANNetplanFile{}, fmt.Errorf("multiple supported netplan configs define network.ethernets.%s: %s", parent, strings.Join(vmLANNetplanFilePaths(matches), ", "))
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(supportedVMLANNetplanFiles(results)) == 0 {
		return vmLANNetplanFile{}, fmt.Errorf("no supported netplan networkd config found for %q: %s", parent, vmLANNetplanUnsupportedReason(errs))
	}
	return vmLANNetplanFile{}, fmt.Errorf("no supported netplan networkd config defines network.ethernets.%s", parent)
}

func readSupportedVMLANNetplanFiles() ([]vmLANNetplanFile, []error) {
	results, errs := scanVMLANNetplanFiles("")
	return supportedVMLANNetplanFiles(results), errs
}

func scanVMLANNetplanFiles(parent string) ([]vmLANNetplanScanResult, []error) {
	paths, globErrs := listVMLANNetplanPaths()
	results := []vmLANNetplanScanResult{}
	errs := append([]error(nil), globErrs...)
	for _, path := range paths {
		result := vmLANNetplanScanResult{file: vmLANNetplanFile{path: path}}
		raw, err := vmLANBridgeReadFileFn(path)
		if err != nil {
			result.err = fmt.Errorf("read %s: %w", path, err)
			errs = append(errs, result.err)
			results = append(results, result)
			continue
		}
		result.file.content = append([]byte(nil), raw...)
		result.definesParent = netplanRawDefinesEthernet(raw, parent)
		doc, err := parseVMLANBridgeNetplan(raw)
		if err != nil {
			result.err = err
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			results = append(results, result)
			continue
		}
		result.file.doc = doc
		if parent != "" {
			_, result.definesParent = doc.Network.Ethernets[parent]
		}
		results = append(results, result)
	}
	if len(paths) == 0 && len(globErrs) == 0 {
		errs = append(errs, fmt.Errorf("no netplan YAML files found in /etc/netplan, /run/netplan, or /lib/netplan"))
	}
	return results, errs
}

func listVMLANNetplanPaths() ([]string, []error) {
	seen := map[string]bool{}
	paths := []string{}
	errs := []error{}
	for _, pattern := range vmLANBridgeNetplanGlobs {
		matches, err := vmLANBridgeNetplanGlobFn(pattern)
		if err != nil {
			errs = append(errs, fmt.Errorf("find netplan configs %s: %w", pattern, err))
			continue
		}
		for _, path := range matches {
			if seen[path] {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return paths, errs
}

func supportedVMLANNetplanFiles(results []vmLANNetplanScanResult) []vmLANNetplanFile {
	files := []vmLANNetplanFile{}
	for _, result := range results {
		if result.err == nil {
			files = append(files, result.file)
		}
	}
	return files
}

func vmLANNetplanFilePaths(files []vmLANNetplanFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.path)
	}
	return paths
}

func vmLANNetplanUnsupportedReason(errs []error) string {
	if len(errs) == 0 {
		return "no supported netplan networkd config found"
	}
	reasons := make([]string, 0, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}
		reasons = append(reasons, err.Error())
	}
	if len(reasons) == 0 {
		return "no supported netplan networkd config found"
	}
	return "no supported netplan networkd config found: " + strings.Join(reasons, "; ")
}

func (r *systemVMLANBridgePrepareRunner) Plan() (vmLANBridgePlan, error) {
	if r.planFn != nil {
		return r.planFn()
	}
	return planSystemVMLANBridge()
}

func (r *systemVMLANBridgePrepareRunner) ReadNetplan(parent string) ([]byte, error) {
	file, err := readVMLANNetplanForParent(parent)
	if err != nil {
		return nil, err
	}
	r.netplanSourcePath = file.path
	return append([]byte(nil), file.content...), nil
}

func (r *systemVMLANBridgePrepareRunner) NetplanSourcePath() string {
	return r.netplanSourcePath
}

func (r *systemVMLANBridgePrepareRunner) WriteNetplanBackup(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create VM LAN bridge netplan backup dir: %w", err)
	}
	if err := writeTextFileAtomically(path, content, 0644); err != nil {
		return fmt.Errorf("write VM LAN bridge netplan backup: %w", err)
	}
	return nil
}

func (r *systemVMLANBridgePrepareRunner) WriteNetplanOverlay(path string, content []byte) error {
	if err := requireManagedVMLANBridgeOverlayContent(content); err != nil {
		return err
	}
	existing, err := vmLANBridgeReadFileFn(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing VM LAN bridge netplan overlay: %w", err)
	}
	if err == nil && !isManagedVMLANBridgeOverlay(existing) {
		return fmt.Errorf("refusing to overwrite unmanaged VM LAN bridge netplan overlay %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create VM LAN bridge netplan overlay dir: %w", err)
	}
	if err := writeTextFileAtomically(path, content, vmLANBridgeNetplanOverlayMode); err != nil {
		return fmt.Errorf("write VM LAN bridge netplan overlay: %w", err)
	}
	return nil
}

func (r *systemVMLANBridgePrepareRunner) Generate() error {
	return vmLANBridgeRunCommandFn("netplan", "generate")
}

func (r *systemVMLANBridgePrepareRunner) Apply() error {
	return vmLANBridgeRunCommandFn("netplan", "apply")
}

func (r *systemVMLANBridgePrepareRunner) Validate(bridge, parent string) error {
	bridge = strings.TrimSpace(bridge)
	parent = strings.TrimSpace(parent)
	var lastPlan vmLANBridgePlan
	var lastErr error
	for attempt := 0; attempt < vmLANBridgeValidationRetries; attempt++ {
		plan, err := r.Plan()
		if err == nil {
			err = validateVMLANBridgeReadyPlan(plan, bridge, parent)
		}
		if err == nil {
			return nil
		}
		lastPlan = plan
		lastErr = err
		if attempt+1 < vmLANBridgeValidationRetries {
			vmLANBridgeSleepFn(vmLANBridgeValidationDelay)
		}
	}
	if lastErr != nil {
		return fmt.Errorf("VM LAN bridge validation failed for bridge %q parent %q: %w", bridge, parent, lastErr)
	}
	return fmt.Errorf("VM LAN bridge validation failed: expected ready bridge %q parent %q, got plan %#v", bridge, parent, lastPlan)
}

func (r *systemVMLANBridgePrepareRunner) ScheduleRollback(id string, after time.Duration) error {
	unit := vmLANBridgeRollbackUnit(id)
	seconds := int(after.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return vmLANBridgeRunCommandFn(
		"systemd-run",
		"--unit", unit,
		"--on-active="+strconv.Itoa(seconds)+"s",
		"--collect",
		"/bin/sh",
		"-c",
		vmLANBridgeRollbackScript(),
	)
}

func (r *systemVMLANBridgePrepareRunner) CancelRollback(id string) error {
	unit := vmLANBridgeRollbackUnit(id)
	return vmLANBridgeRunCommandFn("systemctl", "stop", unit+".timer", unit+".service")
}

func (r *systemVMLANBridgePrepareRunner) Rollback(id string) error {
	remove, err := shouldRemoveVMLANBridgeOverlay(vmLANBridgeNetplanOverlay)
	if err != nil {
		return err
	}
	if remove {
		if err := vmLANBridgeRemoveFileFn(vmLANBridgeNetplanOverlay); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove VM LAN bridge netplan overlay: %w", err)
		}
	}
	if err := vmLANBridgeRunCommandFn("netplan", "apply"); err != nil {
		return err
	}
	return r.CancelRollback(id)
}

func validateVMLANBridgeReadyPlan(plan vmLANBridgePlan, bridge, parent string) error {
	if !plan.Ready || plan.Bridge != bridge {
		return fmt.Errorf("expected ready bridge %q parent %q, got plan %#v", bridge, parent, plan)
	}
	if parent == "" || plan.Parent == parent {
		return nil
	}
	if ok, err := vmLANBridgeParentAttachedToBridge(parent, bridge); err != nil {
		return err
	} else if ok {
		return nil
	}
	return fmt.Errorf("parent %q is not attached to bridge %q", parent, bridge)
}

func vmLANBridgeParentAttachedToBridge(parent, bridge string) (bool, error) {
	links, err := discoverVMLANLinks()
	if err != nil {
		return false, err
	}
	for _, link := range links {
		if link.Name == parent {
			return link.Master == bridge, nil
		}
	}
	return false, fmt.Errorf("parent %q was not found while validating bridge %q", parent, bridge)
}

func requireManagedVMLANBridgeOverlayContent(content []byte) error {
	if !isManagedVMLANBridgeOverlay(content) {
		return fmt.Errorf("VM LAN bridge netplan overlay content is missing managed marker")
	}
	return nil
}

func shouldRemoveVMLANBridgeOverlay(path string) (bool, error) {
	existing, err := vmLANBridgeReadFileFn(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read VM LAN bridge netplan overlay: %w", err)
	}
	if !isManagedVMLANBridgeOverlay(existing) {
		return false, fmt.Errorf("refusing to remove unmanaged VM LAN bridge netplan overlay %s", path)
	}
	return true, nil
}

func isManagedVMLANBridgeOverlay(content []byte) bool {
	return strings.HasPrefix(string(content), vmLANBridgeNetplanMarker+"\n")
}

func vmLANBridgeRollbackScript() string {
	return "p=/etc/netplan/99-yeet-vm-lan-bridge.yaml; " +
		"marker='" + vmLANBridgeNetplanMarker + "'; " +
		"if [ ! -e \"$p\" ] || { IFS= read -r first < \"$p\" && [ \"$first\" = \"$marker\" ]; }; then " +
		"rm -f \"$p\" && netplan apply; " +
		"else echo 'refusing to remove unmanaged VM LAN bridge netplan overlay' >&2; exit 1; fi"
}

func vmLANBridgeRollbackUnit(id string) string {
	id = strings.TrimSpace(id)
	var b strings.Builder
	for _, r := range id {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	unit := strings.Trim(b.String(), "-")
	if unit == "" {
		unit = "rollback"
	}
	return "yeet-" + unit + "-rollback"
}

func defaultVMLANBridgeCommandOutput(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, nil
	}
	if len(output) == 0 {
		return nil, err
	}
	return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
}

func defaultVMLANBridgeRunCommand(name string, args ...string) error {
	output, err := exec.Command(name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if len(output) == 0 {
		return err
	}
	return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
}
