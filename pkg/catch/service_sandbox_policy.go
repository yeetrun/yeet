// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

type serviceSandboxPolicy struct {
	State    string
	ReadOnly []serviceSandboxExposure
	Writable []serviceSandboxExposure
}

type serviceSandboxExposure struct {
	Source      string
	Destination string
}

func serviceSandboxPolicyForExactGeneration(service *db.Service, generation int) (serviceSandboxPolicy, error) {
	if service == nil {
		return serviceSandboxPolicy{}, fmt.Errorf("sandbox policy requires a service")
	}
	if service.Sandbox == nil {
		return serviceSandboxPolicy{State: "legacy"}, nil
	}
	if service.Sandbox.Refs == nil {
		return serviceSandboxPolicy{}, fmt.Errorf("service %q sandbox store has no refs", service.Name)
	}
	stored, ok := service.Sandbox.Refs[db.Gen(generation)]
	if !ok {
		return serviceSandboxPolicy{State: "legacy"}, nil
	}
	if stored == nil {
		return serviceSandboxPolicy{}, fmt.Errorf("service %q generation %d has a nil exact sandbox policy", service.Name, generation)
	}
	if stored.State != "on" && stored.State != "off" {
		return serviceSandboxPolicy{}, fmt.Errorf("service %q generation %d sandbox state %q is invalid", service.Name, generation, stored.State)
	}
	policy := serviceSandboxPolicy{
		State: stored.State, ReadOnly: serviceSandboxExposuresFromDB(stored.ReadOnly),
		Writable: serviceSandboxExposuresFromDB(stored.Writable),
	}
	return normalizeServiceSandboxPolicy(policy)
}

func serviceSandboxExposuresFromDB(stored []db.ServiceSandboxExposure) []serviceSandboxExposure {
	if len(stored) == 0 {
		return nil
	}
	exposures := make([]serviceSandboxExposure, len(stored))
	for index, exposure := range stored {
		exposures[index] = serviceSandboxExposure{
			Source:      exposure.Source,
			Destination: exposure.Destination,
		}
	}
	return exposures
}

func applyServiceSandboxPolicyPatch(service string, current serviceSandboxPolicy, fresh bool, options cli.SandboxOptions) (serviceSandboxPolicy, error) {
	current, requested, err := prepareServiceSandboxPolicyPatch(current, fresh, options)
	if err != nil {
		return serviceSandboxPolicy{}, err
	}
	missingReadOnly, missingWritable := serviceSandboxPatchOmissions(current, requested, options)
	if missingReadOnly || missingWritable {
		return serviceSandboxPolicy{}, sandboxReplacementGuardError(service, current, requested, options, missingReadOnly, missingWritable)
	}
	return requested, nil
}

func prepareServiceSandboxPolicyPatch(current serviceSandboxPolicy, fresh bool, options cli.SandboxOptions) (serviceSandboxPolicy, serviceSandboxPolicy, error) {
	if fresh && current.State == "" {
		current.State = "legacy"
	}
	current, err := normalizeServiceSandboxPolicy(current)
	if err != nil {
		return serviceSandboxPolicy{}, serviceSandboxPolicy{}, fmt.Errorf("normalize current sandbox policy: %w", err)
	}
	requested, err := sandboxPolicyFromOptions(current, fresh, options)
	if err != nil {
		return serviceSandboxPolicy{}, serviceSandboxPolicy{}, err
	}
	requested, err = normalizeServiceSandboxPolicy(requested)
	if err != nil {
		return serviceSandboxPolicy{}, serviceSandboxPolicy{}, err
	}
	return current, requested, nil
}

func serviceSandboxPatchOmissions(current, requested serviceSandboxPolicy, options cli.SandboxOptions) (bool, bool) {
	missingReadOnly := options.ReadOnlySet && !options.ReadOnlyReset && sandboxExposureListOmits(current.ReadOnly, requested.ReadOnly)
	missingWritable := options.WritableSet && !options.WritableReset && sandboxExposureListOmits(current.Writable, requested.Writable)
	return missingReadOnly, missingWritable
}

func sandboxPolicyFromOptions(current serviceSandboxPolicy, fresh bool, options cli.SandboxOptions) (serviceSandboxPolicy, error) {
	state, err := resolveSandboxPatchState(current.State, fresh, options)
	if err != nil {
		return serviceSandboxPolicy{}, err
	}
	policy := serviceSandboxPolicy{State: state, ReadOnly: current.ReadOnly, Writable: current.Writable}
	if fresh {
		policy.ReadOnly = nil
		policy.Writable = nil
	}
	if options.ReadOnlySet {
		policy.ReadOnly = sandboxExposuresFromCLI(options.ReadOnly)
	}
	if options.WritableSet {
		policy.Writable = sandboxExposuresFromCLI(options.Writable)
	}
	return policy, nil
}

func resolveSandboxPatchState(current string, fresh bool, options cli.SandboxOptions) (string, error) {
	if options.StateSet {
		if options.State != "on" && options.State != "off" {
			return "", fmt.Errorf("sandbox state must be on or off")
		}
		return options.State, nil
	}
	if fresh {
		return "on", nil
	}
	if !options.ReadOnlySet && !options.WritableSet {
		return current, nil
	}
	if current == "legacy" {
		return "", fmt.Errorf("legacy sandbox policy requires explicit --sandbox=on or --sandbox=off before editing exposures")
	}
	return "on", nil
}

func sandboxExposuresFromCLI(exposures []cli.SandboxExposure) []serviceSandboxExposure {
	if len(exposures) == 0 {
		return nil
	}
	result := make([]serviceSandboxExposure, len(exposures))
	for index, exposure := range exposures {
		result[index] = serviceSandboxExposure{Source: exposure.Source, Destination: exposure.Destination}
	}
	return result
}

func normalizeServiceSandboxPolicy(policy serviceSandboxPolicy) (serviceSandboxPolicy, error) {
	if policy.State != "legacy" && policy.State != "on" && policy.State != "off" {
		return serviceSandboxPolicy{}, fmt.Errorf("sandbox state %q is invalid", policy.State)
	}
	readOnly, err := normalizeServiceSandboxExposures(policy.ReadOnly, "read-only")
	if err != nil {
		return serviceSandboxPolicy{}, err
	}
	writable, err := normalizeServiceSandboxExposures(policy.Writable, "writable")
	if err != nil {
		return serviceSandboxPolicy{}, err
	}
	if err := validateServiceSandboxDestinationSet(readOnly, writable); err != nil {
		return serviceSandboxPolicy{}, err
	}
	return serviceSandboxPolicy{State: policy.State, ReadOnly: readOnly, Writable: writable}, nil
}

func normalizeServiceSandboxExposures(exposures []serviceSandboxExposure, class string) ([]serviceSandboxExposure, error) {
	if len(exposures) == 0 {
		return nil, nil
	}
	normalized := append([]serviceSandboxExposure(nil), exposures...)
	for _, exposure := range normalized {
		if err := validateServiceSandboxLexicalPath(exposure.Source, "source"); err != nil {
			return nil, fmt.Errorf("%s sandbox exposure: %w", class, err)
		}
		if err := validateServiceSandboxLexicalPath(exposure.Destination, "destination"); err != nil {
			return nil, fmt.Errorf("%s sandbox exposure: %w", class, err)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Destination == normalized[j].Destination {
			return normalized[i].Source < normalized[j].Source
		}
		return normalized[i].Destination < normalized[j].Destination
	})
	return normalized, nil
}

func validateServiceSandboxLexicalPath(path, field string) error {
	if strings.ContainsRune(path, '\x00') {
		return fmt.Errorf("%s contains NUL", field)
	}
	if strings.Contains(path, ":") {
		return fmt.Errorf("%s %q contains an unsupported colon", field, path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s %q must be absolute", field, path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%s %q must be a clean absolute path", field, path)
	}
	return nil
}

func validateServiceSandboxDestinationSet(readOnly, writable []serviceSandboxExposure) error {
	type classifiedExposure struct {
		serviceSandboxExposure
		class string
	}
	all := make([]classifiedExposure, 0, len(readOnly)+len(writable))
	for _, exposure := range readOnly {
		all = append(all, classifiedExposure{serviceSandboxExposure: exposure, class: "read-only"})
	}
	for _, exposure := range writable {
		all = append(all, classifiedExposure{serviceSandboxExposure: exposure, class: "writable"})
	}
	for i := range all {
		for j := 0; j < i; j++ {
			if all[i].Destination == all[j].Destination && all[i].Source == all[j].Source && all[i].class == all[j].class {
				return fmt.Errorf("duplicate sandbox destination %s", all[i].Destination)
			}
			if serviceSandboxDestinationsOverlap(all[i].Destination, all[j].Destination) {
				return fmt.Errorf("sandbox destination %s collides with %s", all[i].Destination, all[j].Destination)
			}
		}
	}
	return nil
}

func serviceSandboxDestinationsOverlap(left, right string) bool {
	if left == right || left == "/" || right == "/" {
		return true
	}
	return strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func sandboxExposureListOmits(existing, requested []serviceSandboxExposure) bool {
	requestedSet := make(map[serviceSandboxExposure]struct{}, len(requested))
	for _, exposure := range requested {
		requestedSet[exposure] = struct{}{}
	}
	for _, exposure := range existing {
		if _, ok := requestedSet[exposure]; !ok {
			return true
		}
	}
	return false
}

func sandboxReplacementGuardError(service string, current, requested serviceSandboxPolicy, options cli.SandboxOptions, missingRO, missingRW bool) error {
	replace := sandboxPatchCommand(service, current, requested, options, true, missingRO, missingRW)
	if err := validateSandboxPreservation(current, requested, options); err != nil {
		return fmt.Errorf("sandbox exposure replacement would remove existing entries without reset; preserving the existing sandbox entries is impossible: %v; replace them with:\n%s", err, replace)
	}
	preserve := sandboxPatchCommand(service, current, requested, options, false, missingRO, missingRW)
	return fmt.Errorf("sandbox exposure replacement would remove existing entries without reset; preserve them with:\n%s\nor replace them with:\n%s", preserve, replace)
}

func validateSandboxPreservation(current, requested serviceSandboxPolicy, options cli.SandboxOptions) error {
	preserved := requested
	if options.ReadOnlySet && !options.ReadOnlyReset {
		preserved.ReadOnly = mergeServiceSandboxExposures(current.ReadOnly, requested.ReadOnly)
	}
	if options.WritableSet && !options.WritableReset {
		preserved.Writable = mergeServiceSandboxExposures(current.Writable, requested.Writable)
	}
	_, err := normalizeServiceSandboxPolicy(preserved)
	return err
}

func sandboxPatchCommand(service string, current, requested serviceSandboxPolicy, options cli.SandboxOptions, replace bool, missingRO, missingRW bool) string {
	args := []string{"yeet", "service", "set", service}
	if options.StateSet {
		args = append(args, "--sandbox="+options.State)
	}
	args = append(args, sandboxClassCommandArgs("--sandbox-ro", current.ReadOnly, requested.ReadOnly, options.ReadOnlySet, options.ReadOnlyReset, replace && missingRO)...)
	args = append(args, sandboxClassCommandArgs("--sandbox-rw", current.Writable, requested.Writable, options.WritableSet, options.WritableReset, replace && missingRW)...)
	for index := range args {
		args[index] = quoteServiceSandboxShellWord(args[index])
	}
	return strings.Join(args, " ")
}

func sandboxClassCommandArgs(flag string, current, requested []serviceSandboxExposure, mentioned, reset, replacementReset bool) []string {
	if !mentioned {
		return nil
	}
	values := requested
	if !reset && !replacementReset {
		values = mergeServiceSandboxExposures(current, requested)
	}
	var args []string
	if reset || replacementReset {
		args = append(args, flag+"=reset")
	}
	for _, exposure := range values {
		args = append(args, flag+"="+formatServiceSandboxExposure(exposure))
	}
	return args
}

func mergeServiceSandboxExposures(left, right []serviceSandboxExposure) []serviceSandboxExposure {
	set := make(map[serviceSandboxExposure]struct{}, len(left)+len(right))
	for _, exposure := range append(append([]serviceSandboxExposure(nil), left...), right...) {
		set[exposure] = struct{}{}
	}
	merged := make([]serviceSandboxExposure, 0, len(set))
	for exposure := range set {
		merged = append(merged, exposure)
	}
	merged, _ = normalizeServiceSandboxExposures(merged, "merged")
	return merged
}

func formatServiceSandboxExposure(exposure serviceSandboxExposure) string {
	if exposure.Source == exposure.Destination {
		return exposure.Source
	}
	return exposure.Source + ":" + exposure.Destination
}

func quoteServiceSandboxShellWord(value string) string {
	if value != "" && strings.IndexFunc(value, serviceSandboxShellWordRuneUnsafe) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func serviceSandboxShellWordRuneUnsafe(value rune) bool {
	isLetter := value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
	isDigit := value >= '0' && value <= '9'
	return !isLetter && !isDigit && !strings.ContainsRune("_@%+=:,./-", value)
}
