// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"path"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type runWebHostStorage struct {
	DataDir      string `json:"dataDir,omitempty"`
	ServicesRoot string `json:"servicesRoot,omitempty"`
}

type runWebHostStorageResponse struct {
	State    string                               `json:"state"`
	Storage  runWebHostStorage                    `json:"storage,omitempty"`
	Defaults catchrpc.ServiceRootDefaultsResponse `json:"defaults,omitempty"`
	Warnings []string                             `json:"warnings,omitempty"`
}

var fetchRunWebHostStorageInfoFn = fetchRunWebHostStorageInfo
var fetchRunWebServiceRootDefaultsFn = fetchRunWebServiceRootDefaults

func fetchRunWebHostStorageInfo(ctx context.Context, host string) (serverInfo, error) {
	var info serverInfo
	err := newRPCClient(host).Call(ctx, "catch.Info", nil, &info)
	return info, err
}

func fetchRunWebServiceRootDefaults(ctx context.Context, host string, req catchrpc.ServiceRootDefaultsRequest) (catchrpc.ServiceRootDefaultsResponse, error) {
	return newRPCClient(host).ServiceRootDefaults(ctx, req)
}

func runWebHostStorageResponseForHost(ctx context.Context, host string, req catchrpc.ServiceRootDefaultsRequest) runWebHostStorageResponse {
	host = strings.TrimSpace(host)
	if host == "" {
		return runWebHostStorageResponse{
			State:    "error",
			Warnings: []string{"no host selected"},
		}
	}
	info, err := fetchRunWebHostStorageInfoFn(ctx, host)
	if err != nil {
		if isRunWebZFSHostUnreachable(err) {
			return runWebHostStorageResponse{State: "host-unreachable"}
		}
		return runWebHostStorageResponse{
			State:    "error",
			Warnings: []string{err.Error()},
		}
	}
	storage := runWebHostStorage{
		DataDir:      strings.TrimSpace(info.RootDir),
		ServicesRoot: strings.TrimSpace(info.ServicesDir),
	}
	if storage.ServicesRoot == "" && storage.DataDir != "" {
		storage.ServicesRoot = path.Join(storage.DataDir, "services")
	}
	if storage.DataDir == "" && storage.ServicesRoot == "" {
		return runWebHostStorageResponse{
			State:    "unknown",
			Warnings: []string{"catch did not report host storage paths"},
		}
	}
	defaults, warnings := runWebHostStorageDefaults(ctx, host, storage, req)
	return runWebHostStorageResponse{
		State:    "available",
		Storage:  storage,
		Defaults: defaults,
		Warnings: warnings,
	}
}

func runWebHostStorageDefaults(ctx context.Context, host string, storage runWebHostStorage, req catchrpc.ServiceRootDefaultsRequest) (catchrpc.ServiceRootDefaultsResponse, []string) {
	req.Service = strings.TrimSpace(req.Service)
	defaults, err := fetchRunWebServiceRootDefaultsFn(ctx, host, req)
	if err == nil {
		return normalizeRunWebServiceRootDefaults(defaults, storage, req.Service), nil
	}
	if isRunWebServiceRootDefaultsUnsupported(err) {
		return runWebHostStorageLegacyDefaults(ctx, host, storage, req.Service), nil
	}
	return runWebFilesystemServiceRootDefaults(storage, req.Service), []string{err.Error()}
}

func isRunWebServiceRootDefaultsUnsupported(err error) bool {
	if isRPCMethodNotFound(err) {
		return true
	}
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unclassified RPC method") && strings.Contains(msg, `"`+catchrpc.RPCMethodServiceRootDefaults+`"`)
}

func normalizeRunWebServiceRootDefaults(defaults catchrpc.ServiceRootDefaultsResponse, storage runWebHostStorage, service string) catchrpc.ServiceRootDefaultsResponse {
	service = strings.TrimSpace(service)
	defaults.ServiceRoot = strings.TrimSpace(defaults.ServiceRoot)
	defaults.ServiceRootZFS = strings.TrimSpace(defaults.ServiceRootZFS)
	defaults.ServiceRootPlaceholder = strings.TrimSpace(defaults.ServiceRootPlaceholder)
	if service == "" {
		return runWebServiceRootPlaceholderDefaults(defaults, storage)
	}
	if defaults.ServiceRoot == "" && defaults.ServiceRootZFS != "" {
		defaults.ServiceRoot = defaults.ServiceRootZFS
	}
	if defaults.ZFS && defaults.ServiceRootZFS == "" {
		defaults.ServiceRootZFS = defaults.ServiceRoot
	}
	if defaults.ServiceRoot == "" {
		return runWebFilesystemServiceRootDefaults(storage, service)
	}
	return defaults
}

func runWebServiceRootPlaceholderDefaults(defaults catchrpc.ServiceRootDefaultsResponse, storage runWebHostStorage) catchrpc.ServiceRootDefaultsResponse {
	if defaults.ZFS {
		root := trimRunWebRootPattern(defaults.ServiceRootZFS)
		if root == "" {
			root = trimRunWebRootPattern(defaults.ServiceRoot)
		}
		if root != "" {
			defaults.ServiceRoot = ""
			defaults.ServiceRootZFS = root
			defaults.ServiceRootPlaceholder = runWebServiceRootPath(root, "<service>")
			return defaults
		}
	}
	root := trimRunWebRootPattern(defaults.ServiceRoot)
	if root == "" {
		root = trimRunWebRootPattern(storage.ServicesRoot)
	}
	if root == "" {
		return catchrpc.ServiceRootDefaultsResponse{}
	}
	return catchrpc.ServiceRootDefaultsResponse{
		ServiceRootPlaceholder: runWebServiceRootPath(root, "<service>"),
	}
}

func runWebFilesystemServiceRootDefaults(storage runWebHostStorage, service string) catchrpc.ServiceRootDefaultsResponse {
	root := strings.TrimSpace(storage.ServicesRoot)
	if root == "" {
		return catchrpc.ServiceRootDefaultsResponse{}
	}
	if strings.TrimSpace(service) == "" {
		return catchrpc.ServiceRootDefaultsResponse{
			ServiceRootPlaceholder: runWebServiceRootPath(root, "<service>"),
		}
	}
	return catchrpc.ServiceRootDefaultsResponse{
		ServiceRoot: runWebServiceRootPath(root, service),
	}
}

func runWebHostStorageLegacyDefaults(ctx context.Context, host string, storage runWebHostStorage, service string) catchrpc.ServiceRootDefaultsResponse {
	resp, err := fetchRunWebZFSRootCandidatesFn(ctx, host, catchrpc.ZFSServiceRootCandidatesRequest{
		Workload: "compose",
		Service:  service,
	})
	if err == nil && resp.State == catchrpc.ZFSRootDiscoveryAvailable {
		if defaults, ok := runWebZFSDefaultForServicesRoot(storage.ServicesRoot, service, resp.Candidates); ok {
			return defaults
		}
	}
	return runWebFilesystemServiceRootDefaults(storage, service)
}

func runWebZFSDefaultForServicesRoot(servicesRoot, service string, candidates []catchrpc.ZFSServiceRootCandidate) (catchrpc.ServiceRootDefaultsResponse, bool) {
	servicesRoot = cleanRunWebRemotePath(servicesRoot)
	if servicesRoot == "" {
		return catchrpc.ServiceRootDefaultsResponse{}, false
	}
	service = strings.TrimSpace(service)
	for _, candidate := range candidates {
		if cleanRunWebRemotePath(candidate.Mountpoint) != servicesRoot {
			continue
		}
		rootDataset := trimRunWebRootPattern(candidate.Dataset)
		serviceRoot := trimRunWebRootPattern(candidate.SuggestedDataset)
		if service == "" {
			if rootDataset == "" {
				rootDataset = serviceRoot
			}
			if rootDataset == "" {
				return catchrpc.ServiceRootDefaultsResponse{}, false
			}
			return catchrpc.ServiceRootDefaultsResponse{
				ServiceRootZFS:         rootDataset,
				ServiceRootPlaceholder: runWebServiceRootPath(rootDataset, "<service>"),
				ZFS:                    true,
			}, true
		}
		if serviceRoot == "" {
			serviceRoot = runWebServiceRootPath(rootDataset, service)
		}
		if serviceRoot == "" {
			return catchrpc.ServiceRootDefaultsResponse{}, false
		}
		return catchrpc.ServiceRootDefaultsResponse{
			ServiceRoot:    serviceRoot,
			ServiceRootZFS: serviceRoot,
			ZFS:            true,
		}, true
	}
	return catchrpc.ServiceRootDefaultsResponse{}, false
}

func trimRunWebRootPattern(root string) string {
	root = strings.TrimSpace(root)
	if root == "/" {
		return root
	}
	return strings.TrimRight(root, "/")
}

func runWebServiceRootPath(root, service string) string {
	root = strings.TrimRight(strings.TrimSpace(root), "/")
	service = strings.TrimSpace(service)
	if root == "" {
		return ""
	}
	if service == "" {
		return root
	}
	if root == "/" {
		return path.Join("/", service)
	}
	return path.Join(root, service)
}

func cleanRunWebRemotePath(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	return path.Clean(root)
}
