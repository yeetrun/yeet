// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/yeetrun/yeet/pkg/db"
)

type dockerNetNSReconciler interface {
	ReconcileNetNS(ctx context.Context) (bool, error)
}

func (s *Server) reconcileNetNSBackedDockerServices(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}

	var errs []error
	for name, sv := range dv.Services().All() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if sv.ServiceType() != db.ServiceTypeDockerCompose {
			continue
		}
		if _, ok := sv.AsStruct().Artifacts.Gen(db.ArtifactNetNSService, sv.Generation()); !ok {
			continue
		}

		service, err := s.newDockerComposeService(sv)
		if err != nil {
			err = fmt.Errorf("load docker compose service %q: %w", name, err)
			log.Printf("netns reconciliation failed for service %q: %v", name, err)
			errs = append(errs, err)
			continue
		}
		restarted, err := service.ReconcileNetNS(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			err = fmt.Errorf("reconcile docker compose service %q: %w", name, err)
			log.Printf("netns reconciliation failed for service %q: %v", name, err)
			errs = append(errs, err)
			continue
		}
		if restarted {
			log.Printf("reconciled stale docker netns for service %q; restarted containers", name)
		}
	}

	return errors.Join(errs...)
}
