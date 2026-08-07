// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"tailscale.com/tailcfg"
)

// desiredServiceNetworkConfig returns persisted desired state, or derives the
// equivalent state from legacy runtime records without mutating the service.
func desiredServiceNetworkConfig(sv db.ServiceView) db.ServiceNetworkConfig {
	if configured := sv.Network(); configured.Valid() {
		return *configured.AsStruct()
	}
	return effectiveServiceNetworkConfig(sv)
}

// effectiveServiceNetworkConfig derives desired-like settings from runtime
// network records for backwards-compatible display of legacy services.
func effectiveServiceNetworkConfig(sv db.ServiceView) db.ServiceNetworkConfig {
	service := sv.AsStruct()
	if service == nil {
		return db.ServiceNetworkConfig{}
	}
	config := db.ServiceNetworkConfig{Modes: effectiveServiceNetworkModes(sv)}
	if service.TSNet != nil {
		config.TSVersion = service.TSNet.Version
		config.TSExitNode = service.TSNet.ExitNode
		config.TSTags = slices.Clone(service.TSNet.Tags)
	}
	if service.Macvlan != nil {
		config.MacvlanParent = service.Macvlan.Parent
		config.MacvlanVLAN = service.Macvlan.VLAN
		config.MacvlanMAC = service.Macvlan.Mac
	}
	return config
}

// applyServiceNetworkPatch applies only explicitly present service-set flags.
func applyServiceNetworkPatch(current db.ServiceNetworkConfig, flags cli.ServiceSetFlags) (db.ServiceNetworkConfig, error) {
	next := current
	next.Modes = slices.Clone(current.Modes)
	next.TSTags = slices.Clone(current.TSTags)
	if flags.NetSet {
		next.Modes = strings.Split(flags.Net, ",")
	}
	if flags.TsVerSet {
		next.TSVersion = flags.TsVer
	}
	if flags.TsExitSet {
		next.TSExitNode = flags.TsExit
	}
	if flags.TsTagsSet {
		next.TSTags = slices.Clone(flags.TsTags)
	}
	if flags.MacvlanParentSet {
		next.MacvlanParent = flags.MacvlanParent
	}
	if flags.MacvlanVlanSet {
		next.MacvlanVLAN = flags.MacvlanVlan
	}
	if flags.MacvlanMacSet {
		next.MacvlanMAC = flags.MacvlanMac
	}
	if err := validatePatchedMacvlanModes(next, flags); err != nil {
		return db.ServiceNetworkConfig{}, err
	}
	return normalizeServiceNetworkConfig(next)
}

func validatePatchedMacvlanModes(cfg db.ServiceNetworkConfig, flags cli.ServiceSetFlags) error {
	if !flags.MacvlanParentSet && !flags.MacvlanVlanSet && !flags.MacvlanMacSet {
		return nil
	}
	modes, err := normalizeDesiredNetworkModes(cfg.Modes)
	if err != nil {
		return err
	}
	if !slices.Contains(modes, "lan") && patchedMacvlanHasValue(cfg, flags) {
		return fmt.Errorf("--macvlan-* settings require LAN networking; use --net=lan or --net=svc,lan")
	}
	return nil
}

func patchedMacvlanHasValue(cfg db.ServiceNetworkConfig, flags cli.ServiceSetFlags) bool {
	return flags.MacvlanParentSet && strings.TrimSpace(cfg.MacvlanParent) != "" ||
		flags.MacvlanVlanSet && cfg.MacvlanVLAN != 0 ||
		flags.MacvlanMacSet && strings.TrimSpace(cfg.MacvlanMAC) != ""
}

// normalizeServiceNetworkConfig validates and canonicalizes desired settings.
func normalizeServiceNetworkConfig(cfg db.ServiceNetworkConfig) (db.ServiceNetworkConfig, error) {
	modes, err := normalizeDesiredNetworkModes(cfg.Modes)
	if err != nil {
		return db.ServiceNetworkConfig{}, err
	}
	tags, err := normalizeTailscaleTags(cfg.TSTags)
	if err != nil {
		return db.ServiceNetworkConfig{}, err
	}
	cfg.TSTags = tags
	if err := validateServiceNetworkModes(modes, cfg); err != nil {
		return db.ServiceNetworkConfig{}, err
	}
	cfg.Modes = modes
	return cfg, nil
}

func normalizeTailscaleTags(rawTags []string) ([]string, error) {
	tags := make([]string, 0, len(rawTags))
	seen := make(map[string]bool, len(rawTags))
	for _, raw := range rawTags {
		tag := strings.TrimSpace(raw)
		if tag == "" || seen[tag] {
			continue
		}
		if err := tailcfg.CheckTag(tag); err != nil {
			return nil, fmt.Errorf("invalid tailscale tag %q: %w", tag, err)
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	return tags, nil
}

func validateServiceNetworkModes(modes []string, cfg db.ServiceNetworkConfig) error {
	if slices.Contains(modes, "host") && len(modes) != 1 {
		return fmt.Errorf("host cannot combine with other network modes")
	}
	if slices.Contains(modes, "iso") && (slices.Contains(modes, "svc") || slices.Contains(modes, "lan")) {
		return fmt.Errorf("iso cannot combine with svc or lan")
	}
	if slices.Contains(modes, "ts") && len(cfg.TSTags) == 0 {
		return fmt.Errorf("tailscale tags are required when network modes include ts")
	}
	if cfg.MacvlanVLAN < 0 || cfg.MacvlanVLAN > 4094 {
		return fmt.Errorf("--macvlan-vlan must be between 1 and 4094")
	}
	return nil
}

// normalizeDesiredNetworkModes adds desired-state host support around the
// shared runtime normalizer, which only admits managed interfaces.
func normalizeDesiredNetworkModes(rawModes []string) ([]string, error) {
	nonHost := make([]string, 0, len(rawModes))
	hasHost := false
	for _, raw := range rawModes {
		if strings.EqualFold(strings.TrimSpace(raw), "host") {
			hasHost = true
			continue
		}
		nonHost = append(nonHost, raw)
	}
	modes, err := iso.NormalizeModes(nonHost)
	if err != nil {
		return nil, err
	}
	if hasHost {
		modes = append(modes, "host")
		sort.Strings(modes)
	}
	return modes, nil
}

// networkOptsFromDesired maps persisted desired settings to installer options.
// authKey is deliberately supplied separately so it cannot be persisted.
func networkOptsFromDesired(cfg db.ServiceNetworkConfig, authKey string) NetworkOpts {
	modes := slices.Clone(cfg.Modes)
	return NetworkOpts{
		Interfaces: strings.Join(modes, ","),
		Modes:      modes,
		ISO:        slices.Contains(modes, "iso"),
		Tailscale: TailscaleOpts{
			Version:  cfg.TSVersion,
			ExitNode: cfg.TSExitNode,
			Tags:     slices.Clone(cfg.TSTags),
			AuthKey:  authKey,
		},
		Macvlan: MacvlanOpts{
			Parent: cfg.MacvlanParent,
			VLAN:   cfg.MacvlanVLAN,
			Mac:    cfg.MacvlanMAC,
		},
	}
}
