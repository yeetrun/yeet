// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/svc"
)

const (
	isoComposeProfileVersion = 1
	isoSysctlProfileVersion  = 1
	isoDockerNetNSRoot       = "/var/run/netns"
)

// ISOComposeAdmissionOptions fixes the host-side context against which a
// canonical Compose model is admitted.
type ISOComposeAdmissionOptions struct {
	ServiceRoot       string
	ProjectName       string
	MaxComponents     int
	RequireISOOverlay *db.ISOAllocation

	volumes map[string]bool
	configs map[string]bool
	secrets map[string]bool
}

// ISOComposeModel is the admitted portion of a canonical Compose model needed
// by ISO allocation and overlay rendering.
type ISOComposeModel struct {
	Components []string
}

type isoCanonicalCompose struct {
	Name     string                     `json:"name"`
	Services map[string]json.RawMessage `json:"services"`
	Networks map[string]json.RawMessage `json:"networks"`
	Volumes  map[string]json.RawMessage `json:"volumes"`
	Configs  map[string]json.RawMessage `json:"configs"`
	Secrets  map[string]json.RawMessage `json:"secrets"`
}

var (
	isoComposeNameRE          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	isoPersistedNetNSRE       = regexp.MustCompile(`^yeet-[0-9a-f]{10}-ns$`)
	inspectISONamespaceHandle = isISONamespaceHandle
	statISOHostResource       = os.Stat
)

var isoAllowedSysctlsV1 = map[string]bool{
	"net.ipv4.ip_unprivileged_port_start": true,
}

var isoAllowedServiceFields = map[string]bool{
	"attach": true, "blkio_config": true, "cap_drop": true, "command": true, "configs": true,
	"container_name": true, "cpu_count": true, "cpu_percent": true, "cpu_period": true,
	"cpu_quota": true, "cpu_rt_period": true, "cpu_rt_runtime": true, "cpu_shares": true,
	"cpus": true, "cpuset": true, "depends_on": true, "deploy": true,
	"entrypoint": true, "env_file": true, "environment": true, "expose": true,
	"extra_hosts": true, "group_add": true, "healthcheck": true,
	"hostname": true, "image": true, "init": true, "labels": true, "logging": true,
	"mac_address": true, "mem_limit": true, "mem_reservation": true, "mem_swappiness": true,
	"memswap_limit": true, "oom_kill_disable": true,
	"oom_score_adj": true, "platform": true, "post_start": true, "pre_stop": true,
	"pids_limit": true, "profiles": true, "pull_policy": true, "read_only": true,
	"restart": true, "scale": true, "secrets": true, "shm_size": true, "stdin_open": true,
	"stop_grace_period": true, "stop_signal": true,
	"sysctls": true, "tmpfs": true, "tty": true, "ulimits": true, "user": true,
	"userns_mode": true, "volumes": true, "working_dir": true,
}

var isoForbiddenServiceFields = map[string]string{
	"build":               "Catch-side builds run before the ISO boundary",
	"cap_add":             "added capabilities can bypass the runtime boundary",
	"cgroup":              "host cgroup namespace sharing is not allowed",
	"cgroup_parent":       "host cgroup placement is not allowed",
	"cpu_rt_period":       "host realtime scheduling is not allowed",
	"cpu_rt_runtime":      "host realtime scheduling is not allowed",
	"devices":             "host devices are not allowed",
	"device_cgroup_rules": "device cgroup rules are not allowed",
	"dns":                 "custom DNS bypasses the ISO resolver",
	"dns_opt":             "custom DNS bypasses the ISO resolver",
	"dns_search":          "custom DNS search bypasses the ISO resolver",
	"domainname":          "custom DNS search is not allowed",
	"external_links":      "external links cross the admitted project",
	"ipc":                 "host or external IPC namespaces are not allowed",
	"links":               "links may cross the generated ISO default network",
	"network_mode":        "alternate network namespaces are not allowed",
	"pid":                 "host or external PID namespaces are not allowed",
	"ports":               "ISO does not support published ports",
	"privileged":          "privileged containers are not allowed",
	"provider":            "host-side providers run before the ISO boundary",
	"runtime":             "custom OCI runtimes are not allowed",
	"security_opt":        "unclassified security options fail closed",
	"storage_opt":         "host storage-driver options are not allowed",
	"uts":                 "host UTS namespace sharing is not allowed",
	"volumes_from":        "cross-container volume inheritance is not allowed",
}

// AdmitISOCompose decodes Docker Compose's canonical JSON model and admits only
// the versioned ISO-safe profile. It intentionally does not parse operator YAML.
func AdmitISOCompose(raw []byte, opts ISOComposeAdmissionOptions) (ISOComposeModel, error) {
	app, err := decodeISOCanonicalCompose(raw)
	if err != nil {
		return ISOComposeModel{}, err
	}
	names, err := admitISOCanonicalCompose(app, opts)
	if err != nil {
		return ISOComposeModel{}, err
	}
	return ISOComposeModel{Components: names}, nil
}

func decodeISOCanonicalCompose(raw []byte) (isoCanonicalCompose, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return isoCanonicalCompose{}, fmt.Errorf("decode canonical Compose JSON: %w", err)
	}
	if top == nil {
		return isoCanonicalCompose{}, fmt.Errorf("decode canonical Compose JSON: top-level value must be an object")
	}
	if err := validateISOTopFields(top); err != nil {
		return isoCanonicalCompose{}, err
	}

	var app isoCanonicalCompose
	if nameRaw, ok := top["name"]; ok {
		if err := json.Unmarshal(nameRaw, &app.Name); err != nil {
			return isoCanonicalCompose{}, fmt.Errorf("name: invalid canonical project name: %w", err)
		}
	}
	if versionRaw, ok := top["version"]; ok {
		var version string
		if err := json.Unmarshal(versionRaw, &version); err != nil {
			return isoCanonicalCompose{}, fmt.Errorf("version: invalid canonical Compose version: %w", err)
		}
	}
	if err := decodeISOCanonicalMaps(top, &app); err != nil {
		return isoCanonicalCompose{}, err
	}
	return app, nil
}

func validateISOTopFields(top map[string]json.RawMessage) error {
	allowed := map[string]bool{
		"name": true, "version": true, "services": true, "networks": true,
		"volumes": true, "configs": true, "secrets": true,
	}
	for _, field := range sortedRawKeys(top) {
		if !allowed[field] {
			return fmt.Errorf("%s: unknown field in ISO safe profile v%d", field, isoComposeProfileVersion)
		}
	}
	return nil
}

func decodeISOCanonicalMaps(top map[string]json.RawMessage, app *isoCanonicalCompose) error {
	var err error
	if app.Services, err = decodeISOMapField(top, "services"); err != nil {
		return err
	}
	if app.Networks, err = decodeISOMapField(top, "networks"); err != nil {
		return err
	}
	if app.Volumes, err = decodeISOMapField(top, "volumes"); err != nil {
		return err
	}
	if app.Configs, err = decodeISOMapField(top, "configs"); err != nil {
		return err
	}
	if app.Secrets, err = decodeISOMapField(top, "secrets"); err != nil {
		return err
	}
	return nil
}

func admitISOCanonicalCompose(app isoCanonicalCompose, opts ISOComposeAdmissionOptions) ([]string, error) {
	if err := validateISOProjectIdentity(app.Name, opts.ProjectName); err != nil {
		return nil, err
	}
	names, err := validateISOComponentNames(app.Services, opts.MaxComponents)
	if err != nil {
		return nil, err
	}
	if err := validateISOProjectDefinitions(app, opts); err != nil {
		return nil, err
	}
	opts.volumes = rawKeySet(app.Volumes)
	opts.configs = rawKeySet(app.Configs)
	opts.secrets = rawKeySet(app.Secrets)
	if err := admitISOServices(names, app.Services, opts); err != nil {
		return nil, err
	}
	return names, nil
}

func validateISOProjectIdentity(canonical, expected string) error {
	if canonical == "" && expected == "" {
		return nil
	}
	if canonical != expected {
		return fmt.Errorf("name: canonical project name %q does not match %q", canonical, expected)
	}
	return nil
}

func validateISOComponentNames(services map[string]json.RawMessage, requestedLimit int) ([]string, error) {
	if len(services) == 0 {
		return nil, fmt.Errorf("services: ISO Compose project has no services")
	}
	names := sortedRawKeys(services)
	for _, name := range names {
		if !isoComposeNameRE.MatchString(name) {
			return nil, fmt.Errorf("services.%s: invalid canonical Compose service name", name)
		}
	}
	limit := isoComponentLimit(requestedLimit)
	if len(names) > limit {
		return nil, fmt.Errorf("services: ISO supports at most %d active components", limit)
	}
	return names, nil
}

func isoComponentLimit(requested int) int {
	if requested > 0 && requested < iso.MaxComponents {
		return requested
	}
	return iso.MaxComponents
}

func validateISOProjectDefinitions(app isoCanonicalCompose, opts ISOComposeAdmissionOptions) error {
	if err := validateISOTopLevelNetworks(app.Networks, opts.ProjectName, opts.RequireISOOverlay); err != nil {
		return err
	}
	if err := validateISOProjectVolumes(app.Volumes, opts.ProjectName); err != nil {
		return err
	}
	if err := validateISOProjectData("configs", app.Configs, opts.ProjectName, opts.ServiceRoot); err != nil {
		return err
	}
	return validateISOProjectData("secrets", app.Secrets, opts.ProjectName, opts.ServiceRoot)
}

func admitISOServices(names []string, services map[string]json.RawMessage, opts ISOComposeAdmissionOptions) error {
	for _, name := range names {
		if err := decodeISOService(name, services[name], opts); err != nil {
			return err
		}
	}
	return nil
}

func decodeISOService(name string, raw json.RawMessage, opts ISOComposeAdmissionOptions) error {
	path := "services." + name
	fields, err := decodeISOObject(path, raw, "canonical service")
	if err != nil {
		return err
	}
	if err := requireISOServiceNetworks(path, fields, opts.RequireISOOverlay); err != nil {
		return err
	}
	if err := validateISOServiceDNS(path+".dns", fields["dns"], opts.RequireISOOverlay); err != nil {
		return err
	}
	for _, field := range sortedRawKeys(fields) {
		if err := validateISOServiceField(path, name, field, fields[field], opts.RequireISOOverlay); err != nil {
			return err
		}
	}
	return validateISOAllowedFields(name, fields, opts)
}

func requireISOServiceNetworks(path string, fields map[string]json.RawMessage, overlay *db.ISOAllocation) error {
	if _, ok := fields["networks"]; !ok {
		if overlay == nil {
			return fmt.Errorf("%s.networks: canonical default network attachment is required", path)
		}
		return fmt.Errorf("%s.networks: persisted ISO overlay attachment is required", path)
	}
	return nil
}

func validateISOServiceField(servicePath, name, field string, value json.RawMessage, overlay *db.ISOAllocation) error {
	path := servicePath + "." + field
	if field == "dns" {
		return nil
	}
	if field == "networks" {
		return validateISOServiceNetworks(path, value, name, overlay)
	}
	if field == "security_opt" {
		if err := validateISOSecurityOptions(path, value); err != nil {
			return err
		}
	}
	if reason, forbidden := isoForbiddenServiceFields[field]; forbidden && !isJSONEmpty(value) {
		return fmt.Errorf("%s: %s", path, reason)
	}
	if !isoAllowedServiceFields[field] && isoForbiddenServiceFields[field] == "" {
		return fmt.Errorf("%s: unknown field in ISO safe profile v%d", path, isoComposeProfileVersion)
	}
	return nil
}

func validateISOServiceDNS(path string, raw json.RawMessage, overlay *db.ISOAllocation) error {
	if overlay == nil {
		if len(raw) == 0 || isJSONEmpty(raw) {
			return nil
		}
		return fmt.Errorf("%s: custom DNS bypasses the ISO resolver", path)
	}
	if len(raw) == 0 {
		return fmt.Errorf("%s: ISO overlay requires exactly one generated DNS resolver", path)
	}
	var resolvers []string
	if err := json.Unmarshal(raw, &resolvers); err != nil || resolvers == nil {
		return fmt.Errorf("%s: invalid canonical DNS representation", path)
	}
	if len(resolvers) != 1 {
		return fmt.Errorf("%s: ISO overlay requires exactly one generated DNS resolver", path)
	}
	if _, err := netip.ParseAddr(resolvers[0]); err != nil {
		return fmt.Errorf("%s: invalid canonical DNS address", path)
	}
	if expected := isoComposeResolver(overlay); resolvers[0] != expected {
		return fmt.Errorf("%s: DNS address does not match generated ISO resolver %q", path, expected)
	}
	return nil
}

func isoComposeResolver(allocation *db.ISOAllocation) string {
	if slices.Contains(allocation.DesiredModes, "ts") {
		return tailscaleDNSIP
	}
	return allocation.Gateway.String()
}

func validateISOAllowedFields(name string, fields map[string]json.RawMessage, opts ISOComposeAdmissionOptions) error {
	prefix := "services." + name + "."
	checks := []struct {
		field    string
		validate func(string, json.RawMessage) error
	}{
		{field: "scale", validate: validateISOScale},
		{field: "deploy", validate: validateISODeploy},
		{field: "userns_mode", validate: validateISOUserNSMode},
		{field: "sysctls", validate: validateISOSysctls},
		{field: "blkio_config", validate: validateISOBlkIO},
		{field: "post_start", validate: validateISOLifecycleHooks},
		{field: "pre_stop", validate: validateISOLifecycleHooks},
		{field: "tmpfs", validate: validateISOServiceTmpfs},
		{field: "volumes", validate: func(path string, raw json.RawMessage) error {
			return validateISOServiceVolumes(path, raw, opts)
		}},
		{field: "configs", validate: func(path string, raw json.RawMessage) error {
			return validateISOServiceResourceRefs(path, raw, opts.configs)
		}},
		{field: "secrets", validate: func(path string, raw json.RawMessage) error {
			return validateISOServiceResourceRefs(path, raw, opts.secrets)
		}},
	}
	for _, check := range checks {
		if err := validateISOOptionalField(prefix+check.field, fields[check.field], check.validate); err != nil {
			return err
		}
	}
	return nil
}

func validateISOOptionalField(path string, raw json.RawMessage, validate func(string, json.RawMessage) error) error {
	if len(raw) == 0 {
		return nil
	}
	return validate(path, raw)
}

func validateISOScale(path string, raw json.RawMessage) error {
	var scale int
	if err := json.Unmarshal(raw, &scale); err != nil || scale != 1 {
		return fmt.Errorf("%s: ISO requires exactly one container", path)
	}
	return nil
}

func validateISOUserNSMode(path string, raw json.RawMessage) error {
	var mode string
	if err := json.Unmarshal(raw, &mode); err != nil {
		return fmt.Errorf("%s: invalid canonical user namespace mode", path)
	}
	if strings.EqualFold(strings.TrimSpace(mode), "host") {
		return fmt.Errorf("%s: host user namespace is not allowed", path)
	}
	return nil
}

func validateISOBlkIO(path string, raw json.RawMessage) error {
	fields, err := decodeISOObject(path, raw, "canonical blkio configuration")
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		"weight": true, "weight_device": true, "device_read_bps": true,
		"device_read_iops": true, "device_write_bps": true, "device_write_iops": true,
	}
	if err := validateISOAllowedKeys(path, fields, allowed, "blkio"); err != nil {
		return err
	}
	for _, field := range sortedRawKeys(fields) {
		if field != "weight" && !isJSONEmpty(fields[field]) {
			return fmt.Errorf("%s.%s: host block devices are not allowed", path, field)
		}
	}
	return nil
}

func validateISOLifecycleHooks(path string, raw json.RawMessage) error {
	var hooks []json.RawMessage
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return fmt.Errorf("%s: invalid canonical lifecycle hooks", path)
	}
	for index, hookRaw := range hooks {
		hookPath := fmt.Sprintf("%s[%d]", path, index)
		if err := validateISOLifecycleHook(hookPath, hookRaw); err != nil {
			return err
		}
	}
	return nil
}

func validateISOLifecycleHook(path string, raw json.RawMessage) error {
	fields, err := decodeISOObject(path, raw, "canonical lifecycle hook")
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		"command": true, "user": true, "privileged": true,
		"working_dir": true, "environment": true,
	}
	if err := validateISOAllowedKeys(path, fields, allowed, "lifecycle hook"); err != nil {
		return err
	}
	return validateISOFalseFlag(path+".privileged", fields["privileged"], "privileged lifecycle hooks are not allowed")
}

func validateISOServiceTmpfs(path string, raw json.RawMessage) error {
	var mounts []string
	if err := json.Unmarshal(raw, &mounts); err != nil {
		return fmt.Errorf("%s: invalid canonical tmpfs representation", path)
	}
	for index, mount := range mounts {
		target := strings.SplitN(mount, ":", 2)[0]
		if !filepath.IsAbs(target) || filepath.Clean(target) != target {
			return fmt.Errorf("%s[%d]: tmpfs target must be a clean absolute container path", path, index)
		}
	}
	return nil
}

func validateISODeploy(path string, raw json.RawMessage) error {
	deploy, err := decodeISOObject(path, raw, "canonical deploy representation")
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		"mode": true, "replicas": true, "endpoint_mode": true, "labels": true,
		"placement": true, "resources": true, "restart_policy": true,
		"rollback_config": true, "update_config": true,
	}
	if err := validateISOAllowedKeys(path, deploy, allowed, "deploy"); err != nil {
		return err
	}
	if err := validateISOOptionalField(path+".replicas", deploy["replicas"], validateISOScale); err != nil {
		return err
	}
	return validateISOOptionalField(path+".resources", deploy["resources"], validateISODeployResources)
}

func validateISODeployResources(path string, raw json.RawMessage) error {
	resources, err := decodeISOObject(path, raw, "canonical deploy resources")
	if err != nil {
		return err
	}
	for _, field := range sortedRawKeys(resources) {
		if field != "limits" && field != "reservations" {
			return fmt.Errorf("%s.%s: unknown deploy resource field in ISO safe profile v%d", path, field, isoComposeProfileVersion)
		}
		if err := validateISODeployResourceClass(path+"."+field, resources[field]); err != nil {
			return err
		}
	}
	return nil
}

func validateISODeployResourceClass(path string, raw json.RawMessage) error {
	values, err := decodeISOObject(path, raw, "canonical deploy resource representation")
	if err != nil {
		return err
	}
	for _, key := range sortedRawKeys(values) {
		switch key {
		case "cpus", "memory", "pids":
		case "devices", "generic_resources":
			return fmt.Errorf("%s.%s: host device resources are not allowed", path, key)
		default:
			return fmt.Errorf("%s.%s: unknown deploy resource field in ISO safe profile v%d", path, key, isoComposeProfileVersion)
		}
	}
	return nil
}

func validateISOSecurityOptions(path string, raw json.RawMessage) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		if strings.Contains(strings.ToLower(one), "unconfined") {
			return fmt.Errorf("%s: unconfined security profiles are not allowed", path)
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return fmt.Errorf("%s: invalid canonical security option representation", path)
	}
	for index, option := range many {
		if strings.Contains(strings.ToLower(option), "unconfined") {
			return fmt.Errorf("%s[%d]: unconfined security profiles are not allowed", path, index)
		}
	}
	return nil
}

func validateISOSysctls(path string, raw json.RawMessage) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return fmt.Errorf("%s: invalid canonical sysctl representation", path)
	}
	for _, name := range sortedRawKeys(values) {
		if !isISOContainerSysctl(name) {
			return fmt.Errorf("%s.%s: sysctl is not allowed by ISO sysctl profile v%d", path, name, isoSysctlProfileVersion)
		}
		var value string
		if err := json.Unmarshal(values[name], &value); err != nil {
			return fmt.Errorf("%s.%s: invalid canonical sysctl value", path, name)
		}
	}
	return nil
}

func isISOContainerSysctl(name string) bool {
	return isoAllowedSysctlsV1[name]
}

func validateISOServiceVolumes(path string, raw json.RawMessage, opts ISOComposeAdmissionOptions) error {
	var mounts []json.RawMessage
	if err := json.Unmarshal(raw, &mounts); err != nil {
		return fmt.Errorf("%s: invalid canonical volume representation", path)
	}
	for index, mountRaw := range mounts {
		mountPath := fmt.Sprintf("%s[%d]", path, index)
		if err := validateISOServiceVolume(mountPath, mountRaw, opts); err != nil {
			return err
		}
	}
	return nil
}

func validateISOServiceVolume(path string, raw json.RawMessage, opts ISOComposeAdmissionOptions) error {
	mount, err := decodeISOObject(path, raw, "canonical volume")
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		"type": true, "source": true, "target": true, "read_only": true,
		"consistency": true, "bind": true, "volume": true, "tmpfs": true,
	}
	if err := validateISOAllowedKeys(path, mount, allowed, "volume"); err != nil {
		return err
	}
	if err := validateISOMountCommon(path, mount); err != nil {
		return err
	}
	var mountType string
	if err := json.Unmarshal(mount["type"], &mountType); err != nil {
		return fmt.Errorf("%s.type: invalid canonical volume type", path)
	}
	if err := validateISOContainerTarget(path+".target", mount["target"]); err != nil {
		return err
	}
	return validateISOServiceVolumeType(path, mountType, mount, opts)
}

func validateISOServiceVolumeType(path, mountType string, mount map[string]json.RawMessage, opts ISOComposeAdmissionOptions) error {
	if err := validateISOMountOptionExclusivity(path, mountType, mount); err != nil {
		return err
	}
	switch mountType {
	case "bind":
		return validateISOBindMount(path, mount, opts.ServiceRoot)
	case "volume":
		return validateISONamedVolumeMount(path, mount, opts.volumes)
	case "tmpfs":
		return validateISOTmpfsMount(path, mount)
	default:
		return fmt.Errorf("%s.type: volume type %q is not allowed", path, mountType)
	}
}

func validateISOMountCommon(path string, mount map[string]json.RawMessage) error {
	if raw, ok := mount["read_only"]; ok {
		var readOnly bool
		if err := json.Unmarshal(raw, &readOnly); err != nil {
			return fmt.Errorf("%s.read_only: invalid canonical boolean", path)
		}
	}
	return rejectISONonemptyField(path+".consistency", mount["consistency"], "mount consistency options are not admitted")
}

func validateISOMountOptionExclusivity(path, mountType string, mount map[string]json.RawMessage) error {
	for _, field := range []string{"bind", "volume", "tmpfs"} {
		raw, present := mount[field]
		if field != mountType && present && !isJSONNull(raw) {
			return fmt.Errorf("%s.%s: option does not match volume type %q", path, field, mountType)
		}
	}
	return nil
}

func validateISOTmpfsMount(path string, mount map[string]json.RawMessage) error {
	if _, ok := mount["source"]; ok {
		return fmt.Errorf("%s.source: tmpfs mounts cannot have a source", path)
	}
	return validateISOTmpfsOptions(path+".tmpfs", mount["tmpfs"])
}

func validateISOContainerTarget(path string, raw json.RawMessage) error {
	var target string
	if len(raw) == 0 || json.Unmarshal(raw, &target) != nil || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return fmt.Errorf("%s: canonical container target must be a clean absolute path", path)
	}
	return nil
}

func validateISOBindMount(path string, mount map[string]json.RawMessage, serviceRoot string) error {
	var source string
	if err := json.Unmarshal(mount["source"], &source); err != nil || source == "" {
		return fmt.Errorf("%s.source: invalid canonical bind source", path)
	}
	if err := validateISOBindOptions(path+".bind", mount["bind"]); err != nil {
		return err
	}
	return validateISOHostPath(path+".source", source, serviceRoot, true)
}

func validateISOBindOptions(path string, raw json.RawMessage) error {
	options, err := decodeISOOptions(path, raw)
	if err != nil {
		return err
	}
	for _, field := range sortedRawKeys(options) {
		if field != "create_host_path" {
			return fmt.Errorf("%s.%s: bind option is not allowed", path, field)
		}
		var enabled bool
		if err := json.Unmarshal(options[field], &enabled); err != nil {
			return fmt.Errorf("%s.%s: invalid canonical bind option", path, field)
		}
	}
	return nil
}

func validateISONamedVolumeMount(path string, mount map[string]json.RawMessage, volumes map[string]bool) error {
	var source string
	if err := json.Unmarshal(mount["source"], &source); err != nil || source == "" {
		return fmt.Errorf("%s.source: project-scoped named volume source is required", path)
	}
	if !volumes[source] {
		return fmt.Errorf("%s.source: named volume %q is not declared by this project", path, source)
	}
	if err := validateISONamedVolumeOptions(path+".volume", mount["volume"]); err != nil {
		return err
	}
	return nil
}

func validateISONamedVolumeOptions(path string, raw json.RawMessage) error {
	options, err := decodeISOOptions(path, raw)
	if err != nil {
		return err
	}
	for _, field := range sortedRawKeys(options) {
		if field != "nocopy" && field != "subpath" {
			return fmt.Errorf("%s.%s: volume option is not allowed", path, field)
		}
	}
	if raw, ok := options["nocopy"]; ok {
		var nocopy bool
		if err := json.Unmarshal(raw, &nocopy); err != nil {
			return fmt.Errorf("%s.nocopy: invalid canonical boolean", path)
		}
	}
	if raw, ok := options["subpath"]; ok {
		if err := validateISOVolumeSubpath(path+".subpath", raw); err != nil {
			return err
		}
	}
	return nil
}

func validateISOVolumeSubpath(path string, raw json.RawMessage) error {
	var subpath string
	if err := json.Unmarshal(raw, &subpath); err != nil || subpath == "" {
		return fmt.Errorf("%s: invalid canonical volume subpath", path)
	}
	clean := filepath.Clean(subpath)
	if filepath.IsAbs(subpath) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s: volume subpath cannot escape its named volume", path)
	}
	return nil
}

func validateISOTmpfsOptions(path string, raw json.RawMessage) error {
	options, err := decodeISOOptions(path, raw)
	if err != nil {
		return err
	}
	for _, field := range sortedRawKeys(options) {
		if field != "size" && field != "mode" {
			return fmt.Errorf("%s.%s: tmpfs option is not allowed", path, field)
		}
	}
	if raw, ok := options["size"]; ok && !isJSONScalarStringOrNumber(raw) {
		return fmt.Errorf("%s.size: invalid canonical tmpfs size", path)
	}
	if raw, ok := options["mode"]; ok {
		var mode uint32
		if err := json.Unmarshal(raw, &mode); err != nil || mode > 0o7777 {
			return fmt.Errorf("%s.mode: invalid canonical tmpfs mode", path)
		}
	}
	return nil
}

func isJSONScalarStringOrNumber(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text != ""
	}
	var number json.Number
	return json.Unmarshal(raw, &number) == nil
}

func decodeISOOptions(path string, raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, nil
	}
	var options map[string]json.RawMessage
	if err := json.Unmarshal(raw, &options); err != nil || options == nil {
		return nil, fmt.Errorf("%s: invalid canonical options", path)
	}
	return options, nil
}

func validateISOServiceResourceRefs(path string, raw json.RawMessage, resources map[string]bool) error {
	var refs []json.RawMessage
	if err := json.Unmarshal(raw, &refs); err != nil {
		return fmt.Errorf("%s: invalid canonical resource references", path)
	}
	for index, refRaw := range refs {
		refPath := fmt.Sprintf("%s[%d]", path, index)
		if err := validateISOServiceResourceRef(refPath, refRaw, resources); err != nil {
			return err
		}
	}
	return nil
}

func validateISOServiceResourceRef(path string, raw json.RawMessage, resources map[string]bool) error {
	ref, err := decodeISOObject(path, raw, "canonical resource reference")
	if err != nil {
		return err
	}
	allowed := map[string]bool{"source": true, "target": true, "uid": true, "gid": true, "mode": true}
	if err := validateISOAllowedKeys(path, ref, allowed, "resource reference"); err != nil {
		return err
	}
	if err := validateISOResourceSource(path+".source", ref["source"], resources); err != nil {
		return err
	}
	if err := validateISOResourceRefStrings(path, ref); err != nil {
		return err
	}
	return validateISOResourceMode(path+".mode", ref["mode"])
}

func validateISOResourceSource(path string, raw json.RawMessage, resources map[string]bool) error {
	var source string
	if err := json.Unmarshal(raw, &source); err != nil || source == "" {
		return fmt.Errorf("%s: invalid canonical resource source", path)
	}
	if !resources[source] {
		return fmt.Errorf("%s: resource %q is not declared by this project", path, source)
	}
	return nil
}

func validateISOResourceRefStrings(path string, ref map[string]json.RawMessage) error {
	for _, field := range []string{"target", "uid", "gid"} {
		value, ok := ref[field]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil || text == "" {
			return fmt.Errorf("%s.%s: invalid canonical resource reference value", path, field)
		}
	}
	return nil
}

func validateISOResourceMode(path string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var mode uint32
	if err := json.Unmarshal(raw, &mode); err != nil {
		return fmt.Errorf("%s: invalid canonical resource mode", path)
	}
	return nil
}

func validateISOProjectVolumes(volumes map[string]json.RawMessage, projectName string) error {
	for _, name := range sortedRawKeys(volumes) {
		if err := validateISOProjectVolume(name, volumes[name], projectName); err != nil {
			return err
		}
	}
	return nil
}

func validateISOProjectVolume(name string, raw json.RawMessage, projectName string) error {
	path := "volumes." + name
	if !isoComposeNameRE.MatchString(name) {
		return fmt.Errorf("%s: invalid canonical volume name", path)
	}
	fields, err := decodeISOObject(path, raw, "canonical volume")
	if err != nil {
		return err
	}
	allowed := map[string]bool{"name": true, "external": true, "driver": true, "driver_opts": true, "labels": true}
	if err := validateISOAllowedKeys(path, fields, allowed, "volume"); err != nil {
		return err
	}
	if err := validateISOFalseFlag(path+".external", fields["external"], "external volumes are not allowed"); err != nil {
		return err
	}
	if err := rejectISONonemptyField(path+".driver", fields["driver"], "custom volume drivers are not allowed"); err != nil {
		return err
	}
	if err := rejectISONonemptyField(path+".driver_opts", fields["driver_opts"], "custom volume driver options are not allowed"); err != nil {
		return err
	}
	return validateProjectScopedName(path+".name", fields["name"], projectName, name)
}

func rejectISONonemptyField(path string, raw json.RawMessage, reason string) error {
	if len(raw) > 0 && !isJSONEmpty(raw) {
		return fmt.Errorf("%s: %s", path, reason)
	}
	return nil
}

func validateISOProjectData(kind string, data map[string]json.RawMessage, projectName, serviceRoot string) error {
	for _, name := range sortedRawKeys(data) {
		if err := validateISOProjectDatum(kind, name, data[name], projectName, serviceRoot); err != nil {
			return err
		}
	}
	return nil
}

func validateISOProjectDatum(kind, name string, raw json.RawMessage, projectName, serviceRoot string) error {
	path := kind + "." + name
	if !isoComposeNameRE.MatchString(name) {
		return fmt.Errorf("%s: invalid canonical resource name", path)
	}
	fields, err := decodeISOObject(path, raw, "canonical resource")
	if err != nil {
		return err
	}
	allowed := map[string]bool{"name": true, "external": true, "file": true, "content": true, "environment": true}
	if err := validateISOAllowedKeys(path, fields, allowed, "resource"); err != nil {
		return err
	}
	if err := validateISOFalseFlag(path+".external", fields["external"], "external resources are not allowed"); err != nil {
		return err
	}
	if err := validateISOOptionalProjectName(path, name, fields["name"], projectName); err != nil {
		return err
	}
	return validateISOProjectDataSources(path, fields, serviceRoot)
}

func validateISOOptionalProjectName(path, key string, raw json.RawMessage, projectName string) error {
	if len(raw) == 0 {
		return nil
	}
	return validateProjectScopedName(path+".name", raw, projectName, key)
}

func validateISOProjectDataSources(path string, fields map[string]json.RawMessage, serviceRoot string) error {
	count := 0
	for _, field := range []string{"file", "content", "environment"} {
		raw, ok := fields[field]
		if !ok {
			continue
		}
		count++
		if err := validateISOProjectDataSource(path+"."+field, field, raw, serviceRoot); err != nil {
			return err
		}
	}
	if count != 1 {
		return fmt.Errorf("%s: canonical resource must have exactly one local source", path)
	}
	return nil
}

func validateISOProjectDataSource(path, kind string, raw json.RawMessage, serviceRoot string) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s: invalid canonical %s source", path, kind)
	}
	if kind == "content" {
		return nil
	}
	if value == "" {
		return fmt.Errorf("%s: canonical %s source cannot be empty", path, kind)
	}
	if kind == "file" {
		return validateISOHostPath(path, value, serviceRoot, false)
	}
	return nil
}

func validateProjectScopedName(path string, raw json.RawMessage, projectName, key string) error {
	var name string
	if len(raw) == 0 || json.Unmarshal(raw, &name) != nil || name != projectName+"_"+key {
		return fmt.Errorf("%s: canonical resource name must be %q", path, projectName+"_"+key)
	}
	return nil
}

func validateISOHostPath(path, source, serviceRoot string, rejectSockets bool) error {
	resolvedSource, err := svc.ValidateISOHostSource(serviceRoot, source)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := rejectISOSpecialHostResource(path, resolvedSource); err != nil {
		return err
	}
	if !rejectSockets {
		return nil
	}
	if err := rejectISOHostControlPath(path, resolvedSource); err != nil {
		return err
	}
	return nil
}

func rejectISOSpecialHostResource(path, source string) error {
	info, err := statISOHostResource(source)
	if err != nil {
		return fmt.Errorf("%s: inspect canonical host resource: %w", path, err)
	}
	if info.Mode()&(os.ModeDevice|os.ModeNamedPipe) != 0 {
		return fmt.Errorf("%s: block devices, character devices, and FIFOs are not allowed", path)
	}
	isNamespace, err := inspectISONamespaceHandle(source)
	if err != nil {
		return fmt.Errorf("%s: inspect canonical host resource filesystem: %w", path, err)
	}
	if isNamespace {
		return fmt.Errorf("%s: namespace handles are not allowed", path)
	}
	return nil
}

func rejectISOHostControlPath(path, source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("%s: inspect canonical bind source: %w", path, err)
	}
	if info.Mode()&os.ModeSocket != 0 || isHostControlPath(source) {
		return fmt.Errorf("%s: host-control sockets and namespace handles are not allowed", path)
	}
	return nil
}

func isHostControlPath(path string) bool {
	clean := filepath.Clean(path)
	for _, suffix := range []string{"docker.sock", "containerd.sock", "podman.sock", "crio.sock", "dockershim.sock"} {
		if strings.HasSuffix(clean, string(filepath.Separator)+suffix) {
			return true
		}
	}
	return false
}

func validateISOTopLevelNetworks(networks map[string]json.RawMessage, projectName string, overlay *db.ISOAllocation) error {
	if len(networks) == 0 {
		if overlay == nil {
			return fmt.Errorf("networks.default: canonical implicit default network is required")
		}
		return fmt.Errorf("networks.default: persisted ISO overlay network is required")
	}
	for _, name := range sortedRawKeys(networks) {
		if name != "default" {
			return fmt.Errorf("networks.%s: ISO allows only the project default network", name)
		}
	}
	if len(networks) != 1 {
		return fmt.Errorf("networks: ISO allows exactly one default network")
	}
	return validateISODefaultNetwork(networks["default"], projectName, overlay)
}

func validateISODefaultNetwork(raw json.RawMessage, projectName string, overlay *db.ISOAllocation) error {
	const path = "networks.default"
	fields, err := decodeISOObject(path, raw, "canonical default network")
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		"name": true, "driver": true, "driver_opts": true, "ipam": true,
		"external": true, "attachable": true, "internal": true,
		"enable_ipv4": true, "enable_ipv6": true, "labels": true,
	}
	if err := validateISOAllowedKeys(path, fields, allowed, "network"); err != nil {
		return err
	}
	if err := validateProjectScopedName(path+".name", fields["name"], projectName, "default"); err != nil {
		return err
	}
	if err := validateISONetworkFlags(path, fields); err != nil {
		return err
	}
	if overlay == nil {
		return validateISOImplicitDefaultNetwork(path, fields)
	}
	return validateISOOverlayNetwork(path, fields, overlay)
}

func validateISONetworkFlags(path string, fields map[string]json.RawMessage) error {
	for _, flag := range []string{"external", "attachable", "internal"} {
		if err := validateISOFalseFlag(path+"."+flag, fields[flag], "ISO does not allow this network override"); err != nil {
			return err
		}
	}
	if err := validateISOFalseFlag(path+".enable_ipv6", fields["enable_ipv6"], "ISO requires IPv6 to be disabled"); err != nil {
		return err
	}
	return validateISOIPv4Enabled(path+".enable_ipv4", fields["enable_ipv4"])
}

func validateISOIPv4Enabled(path string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil || !enabled {
		return fmt.Errorf("%s: ISO requires IPv4 to be enabled", path)
	}
	return nil
}

func validateISOImplicitDefaultNetwork(path string, fields map[string]json.RawMessage) error {
	for _, field := range []string{"driver", "driver_opts"} {
		if err := rejectISONonemptyField(path+"."+field, fields[field], "implicit default network cannot customize "+field); err != nil {
			return err
		}
	}
	if raw, ok := fields["ipam"]; ok && !jsonObjectEmpty(raw) {
		return fmt.Errorf("%s.ipam: implicit default network requires empty IPAM", path)
	}
	return nil
}

func validateISOOverlayNetwork(path string, fields map[string]json.RawMessage, overlay *db.ISOAllocation) error {
	if err := validateISOPersistedOverlay(path, overlay); err != nil {
		return err
	}
	if err := validateISOOverlayDriver(path, fields["driver"]); err != nil {
		return err
	}
	if err := validateISOOverlayDriverOptions(path+".driver_opts", fields["driver_opts"], overlay.NetNS); err != nil {
		return err
	}
	return validateISOOverlayIPAM(path+".ipam", fields["ipam"], overlay.Project.Masked(), overlay.Gateway)
}

func validateISOPersistedOverlay(path string, overlay *db.ISOAllocation) error {
	if !overlay.Project.IsValid() || !overlay.Project.Addr().Is4() || overlay.Project.Bits() != 27 {
		return fmt.Errorf("%s.ipam.config[0].subnet: persisted ISO project must be an IPv4 /27", path)
	}
	if !overlay.Gateway.IsValid() || !overlay.Project.Contains(overlay.Gateway) {
		return fmt.Errorf("%s.ipam.config[0].gateway: persisted ISO gateway is invalid", path)
	}
	if !isoPersistedNetNSRE.MatchString(overlay.NetNS) {
		return fmt.Errorf("%s.driver_opts.dev.catchit.netns: persisted ISO namespace is invalid", path)
	}
	return nil
}

func validateISOOverlayDriver(path string, raw json.RawMessage) error {
	var driver string
	if json.Unmarshal(raw, &driver) != nil || driver != "yeet" {
		return fmt.Errorf("%s.driver: ISO overlay requires the yeet driver", path)
	}
	return nil
}

func validateISOOverlayDriverOptions(path string, raw json.RawMessage, netNS string) error {
	var options map[string]string
	if json.Unmarshal(raw, &options) != nil || options == nil {
		return fmt.Errorf("%s: ISO overlay requires exact driver options", path)
	}
	expectedOptions := map[string]string{
		"dev.catchit.mode":  "iso",
		"dev.catchit.netns": filepath.Join(isoDockerNetNSRoot, netNS),
	}
	for _, key := range sortedStringKeys(options) {
		if _, ok := expectedOptions[key]; !ok {
			return fmt.Errorf("%s.%s: unexpected ISO driver option", path, key)
		}
	}
	for _, key := range sortedStringKeys(expectedOptions) {
		if options[key] != expectedOptions[key] {
			return fmt.Errorf("%s.%s: ISO driver option does not match persisted allocation", path, key)
		}
	}
	return nil
}

func validateISOOverlayIPAM(path string, raw json.RawMessage, project netip.Prefix, gateway netip.Addr) error {
	ipam, err := decodeISOObject(path, raw, "ISO overlay IPAM")
	if err != nil {
		return err
	}
	if err := validateISOAllowedKeys(path, ipam, map[string]bool{"config": true}, "IPAM"); err != nil {
		return err
	}
	var configs []map[string]json.RawMessage
	if err := json.Unmarshal(ipam["config"], &configs); err != nil || len(configs) != 1 {
		return fmt.Errorf("%s.config: ISO overlay requires one persisted IPv4 configuration", path)
	}
	return validateISOOverlayIPAMConfig(path+".config[0]", configs[0], project, gateway)
}

func validateISOOverlayIPAMConfig(path string, config map[string]json.RawMessage, project netip.Prefix, gateway netip.Addr) error {
	allowed := map[string]bool{"subnet": true, "gateway": true}
	if err := validateISOAllowedKeys(path, config, allowed, "IPAM"); err != nil {
		return err
	}
	var subnet string
	if json.Unmarshal(config["subnet"], &subnet) != nil || subnet != project.String() {
		return fmt.Errorf("%s.subnet: ISO subnet does not match persisted allocation", path)
	}
	var gatewayText string
	if json.Unmarshal(config["gateway"], &gatewayText) != nil || gatewayText != gateway.String() {
		return fmt.Errorf("%s.gateway: ISO gateway does not match persisted allocation", path)
	}
	return nil
}

func validateISOServiceNetworks(path string, raw json.RawMessage, serviceName string, overlay *db.ISOAllocation) error {
	networks, err := decodeISOObject(path, raw, "canonical service networks")
	if err != nil {
		return err
	}
	if len(networks) != 1 {
		return fmt.Errorf("%s: ISO service must attach to exactly the default network", path)
	}
	if _, ok := networks["default"]; !ok {
		name := sortedRawKeys(networks)[0]
		return fmt.Errorf("%s.%s: ISO allows only the default network attachment", path, name)
	}
	attachment := networks["default"]
	if overlay == nil {
		return validateISOImplicitServiceNetwork(path+".default", attachment)
	}
	return validateISOOverlayServiceNetwork(path+".default", attachment, serviceName, overlay)
}

func validateISOImplicitServiceNetwork(path string, attachment json.RawMessage) error {
	if !bytes.Equal(bytes.TrimSpace(attachment), []byte("null")) {
		return fmt.Errorf("%s: implicit default network attachment must be null", path)
	}
	return nil
}

func validateISOOverlayServiceNetwork(path string, attachment json.RawMessage, serviceName string, overlay *db.ISOAllocation) error {
	component, ok := overlay.Components[serviceName]
	if !ok || !component.Address.IsValid() || !overlay.Project.Contains(component.Address) {
		return fmt.Errorf("%s.ipv4_address: persisted ISO component address is missing or invalid", path)
	}
	fields, err := decodeISOObject(path, attachment, "ISO overlay attachment")
	if err != nil {
		return err
	}
	if err := validateISOAllowedKeys(path, fields, map[string]bool{"ipv4_address": true}, "overlay attachment"); err != nil {
		return err
	}
	var address string
	if json.Unmarshal(fields["ipv4_address"], &address) != nil || address != component.Address.String() {
		return fmt.Errorf("%s.ipv4_address: ISO address does not match persisted component allocation", path)
	}
	return nil
}

func sortedRawKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func rawKeySet(values map[string]json.RawMessage) map[string]bool {
	set := make(map[string]bool, len(values))
	for key := range values {
		set[key] = true
	}
	return set
}

func decodeISOMapField(top map[string]json.RawMessage, field string) (map[string]json.RawMessage, error) {
	raw, ok := top[field]
	if !ok {
		return nil, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, fmt.Errorf("%s: canonical field must be an object", field)
	}
	return values, nil
}

func decodeISOObject(path string, raw json.RawMessage, description string) (map[string]json.RawMessage, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, fmt.Errorf("%s: %s must be an object", path, description)
	}
	return values, nil
}

func validateISOAllowedKeys(path string, values map[string]json.RawMessage, allowed map[string]bool, kind string) error {
	for _, field := range sortedRawKeys(values) {
		if !allowed[field] {
			return fmt.Errorf("%s.%s: unknown %s field in ISO safe profile v%d", path, field, kind, isoComposeProfileVersion)
		}
	}
	return nil
}

func isJSONEmpty(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("false")) || bytes.Equal(trimmed, []byte("0")) || bytes.Equal(trimmed, []byte(`""`)) {
		return true
	}
	return bytes.Equal(trimmed, []byte("[]")) || bytes.Equal(trimmed, []byte("{}"))
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func jsonObjectEmpty(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && len(value) == 0
}

func validateISOFalseFlag(path string, raw json.RawMessage, reason string) error {
	if len(raw) == 0 {
		return nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s: invalid canonical boolean", path)
	}
	if value {
		return fmt.Errorf("%s: %s", path, reason)
	}
	return nil
}
