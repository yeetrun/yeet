// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package serviceid validates portable yeet service identifiers.
package serviceid

import "fmt"

const (
	// MaxLength keeps service IDs compatible with DNS-label use in service networking.
	MaxLength = 63
	// Rules describes the accepted service name syntax for user-facing errors.
	Rules = "use 1-63 lowercase letters, numbers, and dashes; start with a letter and end with a letter or number"
)

// Validate returns an error when name is not safe across catch service roots,
// systemd units, Docker Compose project names, and yeet service-network DNS.
func Validate(name string) error {
	if name == "" {
		return fmt.Errorf("service name is required")
	}
	if len(name) > MaxLength || !isLowerLetter(name[0]) || !isLowerLetterOrDigit(name[len(name)-1]) {
		return invalid(name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if isLowerLetterOrDigit(c) || c == '-' {
			continue
		}
		return invalid(name)
	}
	return nil
}

func invalid(name string) error {
	return fmt.Errorf("invalid service name %q: %s", name, Rules)
}

func isLowerLetter(c byte) bool {
	return c >= 'a' && c <= 'z'
}

func isLowerLetterOrDigit(c byte) bool {
	return isLowerLetter(c) || c >= '0' && c <= '9'
}
