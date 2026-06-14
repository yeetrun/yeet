// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func (s *Server) restoreRecoveryPoint(ctx context.Context, serviceName, selector string, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) error {
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
		return s.restoreServiceRootRecoveryPoint(ctx, service, point, flags, rw)
	}
	return s.restoreVMRecoveryPoint(ctx, service, point, flags, rw)
}
