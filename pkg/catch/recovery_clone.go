// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func (s *Server) cloneRecoveryPoint(ctx context.Context, serviceName, selector, newServiceName string, flags cli.SnapshotsCloneFlags, w io.Writer) error {
	if err := validateRecoveryCloneServiceName(newServiceName); err != nil {
		return err
	}
	initialService, err := s.recoveryService(serviceName)
	if err != nil {
		return err
	}
	if initialService.ServiceType != db.ServiceTypeVM && strings.TrimSpace(initialService.ServiceRootZFS) == "" {
		return fmt.Errorf("service %s is not backed by a ZFS service root", initialService.Name)
	}
	point, service, err := s.resolveRecoveryPoint(ctx, serviceName, selector)
	if err != nil {
		return err
	}
	if service.ServiceType != db.ServiceTypeVM {
		return s.cloneServiceRootRecoveryPoint(ctx, service, point, newServiceName, flags, w)
	}
	return s.cloneVMRecoveryPoint(ctx, service, point, newServiceName, flags, w)
}

func validateRecoveryCloneServiceName(name string) error {
	trimmed := strings.TrimSpace(name)
	switch {
	case trimmed == "":
		return fmt.Errorf("invalid clone target service name %q: name is required", name)
	case trimmed != name:
		return fmt.Errorf("invalid clone target service name %q: contains whitespace or control characters", name)
	case isReservedRecoveryCloneServiceName(trimmed):
		return fmt.Errorf("invalid clone target service name %q: reserved service name", trimmed)
	case hasUnsafeRecoveryCloneServiceNameChars(trimmed):
		return fmt.Errorf("invalid clone target service name %q: contains unsafe path or dataset characters", trimmed)
	case hasWhitespaceOrControl(trimmed):
		return fmt.Errorf("invalid clone target service name %q: contains whitespace or control characters", trimmed)
	}
	return nil
}

func isReservedRecoveryCloneServiceName(name string) bool {
	for _, reserved := range []string{"catch", "sys", "system", "default"} {
		if name == reserved {
			return true
		}
	}
	return false
}

func hasUnsafeRecoveryCloneServiceNameChars(name string) bool {
	return name == "." || strings.Contains(name, "..") || strings.ContainsAny(name, `/\@`)
}

func hasWhitespaceOrControl(name string) bool {
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}
