// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
)

type RunDraft struct {
	Service        string            `json:"service"`
	Host           string            `json:"host"`
	Payload        string            `json:"payload"`
	PayloadKind    string            `json:"payloadKind,omitempty"`
	EnvFile        string            `json:"envFile,omitempty"`
	EnvFileArg     string            `json:"-"`
	EnvFileSet     bool              `json:"-"`
	PayloadArgs    []string          `json:"payloadArgs,omitempty"`
	Network        RunDraftNetwork   `json:"network"`
	Storage        RunDraftStorage   `json:"storage"`
	Snapshots      RunDraftSnapshots `json:"snapshots"`
	NewServiceOnly bool              `json:"newServiceOnly,omitempty"`
	ExistingEntry  ServiceEntry      `json:"-"`
}

type RunDraftNetwork struct {
	Modes         []string `json:"modes,omitempty"`
	TSVersion     string   `json:"tsVersion,omitempty"`
	TSExitNode    string   `json:"tsExitNode,omitempty"`
	TSTags        []string `json:"tsTags,omitempty"`
	TSAuthKey     string   `json:"tsAuthKey,omitempty"`
	MacvlanMAC    string   `json:"macvlanMac,omitempty"`
	MacvlanVLAN   int      `json:"macvlanVlan,omitempty"`
	MacvlanParent string   `json:"macvlanParent,omitempty"`
	Restart       bool     `json:"restart"`
	Publish       []string `json:"publish,omitempty"`
}

type RunDraftStorage struct {
	ServiceRoot string `json:"serviceRoot,omitempty"`
	ZFS         bool   `json:"zfs,omitempty"`
}

type RunDraftSnapshots struct {
	Mode            string   `json:"mode,omitempty"`
	KeepLast        int      `json:"keepLast,omitempty"`
	KeepLastInherit bool     `json:"keepLastInherit,omitempty"`
	MaxAge          string   `json:"maxAge,omitempty"`
	MaxAgeInherit   bool     `json:"maxAgeInherit,omitempty"`
	Required        *bool    `json:"required,omitempty"`
	RequiredInherit bool     `json:"requiredInherit,omitempty"`
	Events          []string `json:"events,omitempty"`
	EventsInherit   bool     `json:"eventsInherit,omitempty"`
}

func runDraftFromCLI(cmdArgs []string, cfgLoc *projectConfigLocation, hostOverride string) (RunDraft, error) {
	payload, runArgs, err := splitRunPayloadArgs(cmdArgs)
	if err != nil {
		return RunDraft{}, err
	}
	flags, err := parseSvcRunControlFlags(runArgs)
	if err != nil {
		return RunDraft{}, err
	}
	parsedFlags, _, err := cli.ParseRun(flags.Args)
	if err != nil {
		return RunDraft{}, err
	}
	if parsedFlags.Web {
		return RunDraft{}, fmt.Errorf("--web starts the local web deploy UI and cannot be forwarded to catch")
	}
	entry, hasEntry := serviceEntryForConfig(cfgLoc, hostOverride)
	effectiveArgs, err := effectiveSvcRunArgs(entry, hasEntry, flags.Args)
	if err != nil {
		return RunDraft{}, err
	}
	if err := ensureSvcRunEntryFlags(entry, hasEntry, effectiveArgs); err != nil {
		return RunDraft{}, err
	}
	effectiveParsed, _, err := cli.ParseRun(effectiveArgs)
	if err != nil {
		return RunDraft{}, err
	}
	envFile := svcRunEnvFile(flags, entry, hasEntry, cfgLoc)
	serviceRoot, serviceRootZFS := svcRunServiceRoot(flags, entry, hasEntry)
	snapshots := snapshotOptionsForSvcRun(entry, flags)
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		host = Host()
	}

	return RunDraft{
		Service:       getService(),
		Host:          host,
		Payload:       payload,
		EnvFile:       envFile,
		EnvFileArg:    flags.EnvFileArg,
		EnvFileSet:    flags.EnvFileSet,
		PayloadArgs:   payloadArgsFromRunArgs(effectiveArgs),
		Network:       runDraftNetworkFromRunFlags(effectiveParsed),
		Storage:       RunDraftStorage{ServiceRoot: serviceRoot, ZFS: serviceRootZFS},
		Snapshots:     runDraftSnapshotsFromOptions(snapshots),
		ExistingEntry: entry,
	}, nil
}

func (d RunDraft) runArgs() []string {
	args := runArgsFromDraftNetwork(d.Network)
	args = runArgsWithDraftSnapshotOptions(args, d.Snapshots)
	args = runArgsWithServiceRootOptions(args, serviceRootOptions{
		Root: d.Storage.ServiceRoot,
		ZFS:  d.Storage.ZFS,
	})
	if len(d.PayloadArgs) != 0 {
		args = append(args, "--")
		args = append(args, d.PayloadArgs...)
	}
	return args
}

func runArgsFromDraftNetwork(n RunDraftNetwork) []string {
	var args []string
	args = appendRunDraftStringFlag(args, "--net", joinRunDraftModes(n.Modes))
	args = appendRunDraftStringFlag(args, "--ts-ver", n.TSVersion)
	args = appendRunDraftStringFlag(args, "--ts-exit", n.TSExitNode)
	args = appendRunDraftRepeatedFlag(args, "--ts-tags", n.TSTags)
	args = appendRunDraftStringFlag(args, "--ts-auth-key", n.TSAuthKey)
	args = appendRunDraftStringFlag(args, "--macvlan-mac", n.MacvlanMAC)
	args = appendRunDraftIntFlag(args, "--macvlan-vlan", n.MacvlanVLAN)
	args = appendRunDraftStringFlag(args, "--macvlan-parent", n.MacvlanParent)
	args = appendRunDraftRestartFlag(args, n.Restart)
	return appendRunDraftRepeatedFlag(args, "--publish", n.Publish)
}

func payloadArgsFromRunArgs(args []string) []string {
	_, payloadArgs := splitRunArgsForParsing(args)
	return append([]string{}, payloadArgs...)
}

func splitRunModes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		mode := strings.TrimSpace(part)
		if mode != "" {
			out = append(out, mode)
		}
	}
	return out
}

func joinRunDraftModes(modes []string) string {
	return strings.Join(splitRunModes(strings.Join(modes, ",")), ",")
}

func appendRunDraftStringFlag(args []string, name, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return args
	}
	return append(args, name+"="+value)
}

func appendRunDraftIntFlag(args []string, name string, value int) []string {
	if value == 0 {
		return args
	}
	return append(args, fmt.Sprintf("%s=%d", name, value))
}

func appendRunDraftRepeatedFlag(args []string, name string, values []string) []string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			args = append(args, name+"="+value)
		}
	}
	return args
}

func appendRunDraftRestartFlag(args []string, restart bool) []string {
	if restart {
		return args
	}
	return append(args, "--restart=false")
}

func runDraftNetworkFromRunFlags(flags cli.RunFlags) RunDraftNetwork {
	return RunDraftNetwork{
		Modes:         splitRunModes(flags.Net),
		TSVersion:     flags.TsVer,
		TSExitNode:    flags.TsExit,
		TSTags:        append([]string{}, flags.TsTags...),
		TSAuthKey:     flags.TsAuthKey,
		MacvlanMAC:    flags.MacvlanMac,
		MacvlanVLAN:   flags.MacvlanVlan,
		MacvlanParent: flags.MacvlanParent,
		Restart:       flags.Restart,
		Publish:       append([]string{}, flags.Publish...),
	}
}

func runDraftSnapshotsFromOptions(opts snapshotOptions) RunDraftSnapshots {
	return RunDraftSnapshots{
		Mode:            opts.Snapshots,
		KeepLast:        opts.KeepLast,
		KeepLastInherit: opts.KeepLastInherit,
		MaxAge:          opts.MaxAge,
		MaxAgeInherit:   opts.MaxAgeInherit,
		Required:        cloneBoolPtr(opts.Required),
		RequiredInherit: opts.RequiredInherit,
		Events:          append([]string{}, opts.Events...),
		EventsInherit:   opts.EventsInherit,
	}
}

func runArgsWithDraftSnapshotOptions(args []string, snapshots RunDraftSnapshots) []string {
	if snapshots.EventsInherit || len(snapshots.Events) != 0 {
		args = runArgsWithSnapshotOptions(args, snapshotOptions{
			Events:        snapshots.Events,
			EventsInherit: snapshots.EventsInherit,
		})
	}
	if snapshots.RequiredInherit || snapshots.Required != nil {
		args = runArgsWithSnapshotOptions(args, snapshotOptions{
			Required:        snapshots.Required,
			RequiredInherit: snapshots.RequiredInherit,
		})
	}
	if snapshots.MaxAgeInherit || snapshots.MaxAge != "" {
		args = runArgsWithSnapshotOptions(args, snapshotOptions{
			MaxAge:        snapshots.MaxAge,
			MaxAgeInherit: snapshots.MaxAgeInherit,
		})
	}
	if snapshots.KeepLastInherit || snapshots.KeepLast != 0 {
		args = runArgsWithSnapshotOptions(args, snapshotOptions{
			KeepLast:        snapshots.KeepLast,
			KeepLastInherit: snapshots.KeepLastInherit,
		})
	}
	if snapshots.Mode != "" {
		args = runArgsWithSnapshotOptions(args, snapshotOptions{Snapshots: snapshots.Mode})
	}
	return args
}
