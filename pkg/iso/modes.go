// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iso

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrPublishedPorts reports that host-published ports are incompatible with ISO.
var ErrPublishedPorts = errors.New("iso does not support published ports")

type PayloadKind string

const (
	PayloadVM        PayloadKind = "vm"
	PayloadCompose   PayloadKind = "compose"
	PayloadContainer PayloadKind = "container"
	PayloadNative    PayloadKind = "native"
	PayloadCron      PayloadKind = "cron"
)

type NetworkRequest struct {
	Payload   PayloadKind
	Modes     []string
	Published bool
}

func NormalizeModes(modes []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(modes))
	for _, raw := range modes {
		mode := strings.ToLower(strings.TrimSpace(raw))
		if mode == "" {
			return nil, fmt.Errorf("network mode cannot be empty")
		}
		if mode != "host" && mode != "svc" && mode != "lan" && mode != "ts" && mode != "iso" {
			return nil, fmt.Errorf("unsupported network mode %q", raw)
		}
		if !seen[mode] {
			seen[mode] = true
			out = append(out, mode)
		}
	}
	sort.Strings(out)
	return out, nil
}

func ValidateNetwork(req NetworkRequest) error {
	modes, err := NormalizeModes(req.Modes)
	if err != nil {
		return err
	}
	if hasMode(modes, "host") && len(modes) != 1 {
		return fmt.Errorf("host cannot combine with other network modes")
	}
	if !hasMode(modes, "iso") {
		return nil
	}
	if err := validateISOCombinations(req, modes); err != nil {
		return err
	}
	return validateISOPayload(req.Payload, modes)
}

func hasMode(modes []string, want string) bool {
	for _, mode := range modes {
		if mode == want {
			return true
		}
	}
	return false
}

func validateISOCombinations(req NetworkRequest, modes []string) error {
	if hasMode(modes, "svc") || hasMode(modes, "lan") {
		return fmt.Errorf("iso cannot combine with svc or lan")
	}
	if req.Published {
		return ErrPublishedPorts
	}
	return nil
}

func validateISOPayload(payload PayloadKind, modes []string) error {
	switch payload {
	case PayloadVM:
		return validateVMISOModes(modes)
	case PayloadCompose, PayloadContainer:
		return validateContainerISOModes(modes)
	case PayloadNative:
		return validateSingleISOMode(modes, "native")
	case PayloadCron:
		return validateSingleISOMode(modes, "timer")
	default:
		return fmt.Errorf("payload kind %q does not support iso", payload)
	}

}

func validateVMISOModes(modes []string) error {
	if len(modes) != 1 {
		return fmt.Errorf("VMs support only iso as a Yeet-managed isolated mode")
	}
	return nil
}

func validateContainerISOModes(modes []string) error {
	if len(modes) > 2 || len(modes) == 2 && !hasMode(modes, "ts") {
		return fmt.Errorf("container ISO modes must be iso or iso,ts")
	}
	return nil
}

func validateSingleISOMode(modes []string, payload string) error {
	if len(modes) != 1 {
		return fmt.Errorf("%s ISO supports only iso", payload)
	}
	return nil
}
