// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"log"

	"github.com/yeetrun/yeet/pkg/db"
)

type dockerNetNSReconciler interface {
	ReconcileNetNS() (bool, error)
}

func (s *Server) reconcileNetNSBackedDockerServices() error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}

	for name, sv := range dv.Services().All() {
		if sv.ServiceType() != db.ServiceTypeDockerCompose {
			continue
		}
		if _, ok := sv.AsStruct().Artifacts.Gen(db.ArtifactNetNSService, sv.Generation()); !ok {
			continue
		}

		service, err := s.newDockerComposeService(sv)
		if err != nil {
			return fmt.Errorf("load docker compose service %q: %w", name, err)
		}
		restarted, err := service.ReconcileNetNS()
		if err != nil {
			return fmt.Errorf("reconcile docker compose service %q: %w", name, err)
		}
		if restarted {
			log.Printf("reconciled stale docker netns for service %q; restarted containers", name)
		}
	}

	return nil
}
