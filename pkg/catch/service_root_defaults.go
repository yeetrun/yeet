// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"path"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func (s *Server) serviceRootDefaults(ctx context.Context, req catchrpc.ServiceRootDefaultsRequest) (catchrpc.ServiceRootDefaultsResponse, error) {
	servicesRoot := s.configuredServicesRoot()
	if servicesRoot == "" {
		return catchrpc.ServiceRootDefaultsResponse{}, nil
	}
	service := strings.TrimSpace(req.Service)
	dataset, ok, err := zfsDatasetForMountpoint(ctx, s.zfsRunner, servicesRoot)
	if err != nil {
		return catchrpc.ServiceRootDefaultsResponse{}, err
	}
	if ok {
		root := suggestedZFSDataset(dataset, service)
		return catchrpc.ServiceRootDefaultsResponse{
			ServiceRoot:    root,
			ServiceRootZFS: root,
			ZFS:            true,
		}, nil
	}
	return catchrpc.ServiceRootDefaultsResponse{
		ServiceRoot: defaultServiceRootFromServicesRoot(servicesRoot, service),
	}, nil
}

func (s *Server) configuredServicesRoot() string {
	if s == nil {
		return ""
	}
	if root := strings.TrimSpace(s.cfg.ServicesRoot); root != "" {
		return root
	}
	if dataDir := strings.TrimSpace(s.cfg.RootDir); dataDir != "" {
		return filepath.Join(dataDir, "services")
	}
	return ""
}

func defaultServiceRootFromServicesRoot(servicesRoot, service string) string {
	servicesRoot = strings.TrimRight(strings.TrimSpace(servicesRoot), "/")
	service = strings.TrimSpace(service)
	if servicesRoot == "" {
		return ""
	}
	if service == "" {
		return servicesRoot
	}
	if servicesRoot == "/" {
		return path.Join("/", service)
	}
	return path.Join(servicesRoot, service)
}
