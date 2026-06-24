// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	vmRuntimeFirecracker = "firecracker"

	vmDiskBackendRaw  = "raw"
	vmDiskBackendZVOL = "zvol"

	vmBalloonModeAuto = "auto"
	vmBalloonModeOff  = "off"

	vmHostMemoryPolicySafe       = "safe"
	vmHostMemoryPolicyBalanced   = "balanced"
	vmHostMemoryPolicyAggressive = "aggressive"
)

type vmShape struct {
	CPUs           int
	MemoryBytes    int64
	MinMemoryBytes int64
	BalloonMode    string
	DiskBytes      int64
	DiskBackend    string
}

func parseVMSize(raw string) (int64, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, nil
	}
	original := raw
	mult := int64(1)
	suffix := ""
	switch {
	case strings.HasSuffix(raw, "gb"):
		mult = 1 << 30
		suffix = "gb"
	case strings.HasSuffix(raw, "g"):
		mult = 1 << 30
		suffix = "g"
	case strings.HasSuffix(raw, "mb"):
		mult = 1 << 20
		suffix = "mb"
	case strings.HasSuffix(raw, "m"):
		mult = 1 << 20
		suffix = "m"
	}
	if suffix != "" {
		raw = strings.TrimSuffix(raw, suffix)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q", original)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("size overflows int64: %q", original)
	}
	return n * mult, nil
}
