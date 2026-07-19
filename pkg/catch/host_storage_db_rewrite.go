// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

type hostStorageDBRewriteResult struct {
	Changed int
}

func hostStorageDBRewriteMappingsFromPlan(plan catchrpc.HostStoragePlan) hostStoragePathMappings {
	mappings := hostStorageMappingsFromPlan(plan)
	if plan.DataDirAction.Move &&
		strings.TrimSpace(plan.Current.ServicesRoot) != "" &&
		hostStoragePathsEqual(plan.Current.ServicesRoot, plan.Desired.ServicesRoot) {
		mappings = append(mappings, hostStoragePathMapping{
			From:   plan.Current.ServicesRoot,
			To:     plan.Current.ServicesRoot,
			Reason: hostStoragePathReasonServicesDir,
		})
	}
	return mappings.Sorted()
}

func rewriteHostStorageDataPaths(data *db.Data, mappings hostStoragePathMappings) (hostStorageDBRewriteResult, error) {
	var result hostStorageDBRewriteResult
	if data == nil || len(mappings) == 0 {
		return result, nil
	}
	serviceKeys := make([]string, 0, len(data.Services))
	for key := range data.Services {
		serviceKeys = append(serviceKeys, key)
	}
	slices.Sort(serviceKeys)
	for _, key := range serviceKeys {
		service := data.Services[key]
		if service == nil {
			continue
		}
		changed, err := rewriteHostStorageServiceDataPaths(key, service, mappings)
		if err != nil {
			return result, err
		}
		result.Changed += changed
	}
	return result, nil
}

func rewriteHostStorageServiceDataPaths(key string, service *db.Service, mappings hostStoragePathMappings) (int, error) {
	serviceName := service.Name
	if serviceName == "" {
		serviceName = key
	}
	serviceScopedMappings := hostStorageServiceScopedRewriteMappings(mappings)
	changed, err := rewriteHostStorageArtifactRefs(serviceName, service.Artifacts, mappings)
	if err != nil || service.VM == nil {
		return changed, err
	}
	vmChanged, err := rewriteHostStorageVMPaths(serviceName, service.VM, mappings, serviceScopedMappings)
	return changed + vmChanged, err
}

func rewriteHostStorageVMPaths(serviceName string, vm *db.VMConfig, mappings, serviceScopedMappings hostStoragePathMappings) (int, error) {
	changed, err := rewriteHostStorageVMFields(serviceName, mappings, []hostStorageRewriteField{
		{name: "VM.Image.Kernel", value: &vm.Image.Kernel},
		{name: "VM.Image.RootFS", value: &vm.Image.RootFS},
	})
	if err != nil {
		return changed, err
	}
	runtimeChanged, err := rewriteHostStorageVMRuntimePaths(serviceName, vm.Components, mappings)
	if err != nil {
		return changed, err
	}
	serviceChanged, err := rewriteHostStorageVMFields(serviceName, serviceScopedMappings, []hostStorageRewriteField{
		{name: "VM.Disk.Path", value: &vm.Disk.Path},
		{name: "VM.Console.SocketPath", value: &vm.Console.SocketPath},
		{name: "VM.Console.LogPath", value: &vm.Console.LogPath},
		{name: "VM.Sockets.APISocketPath", value: &vm.Sockets.APISocketPath},
		{name: "VM.Sockets.VsockSocketPath", value: &vm.Sockets.VsockSocketPath},
		{name: "VM.PIDFile", value: &vm.PIDFile},
	})
	return changed + runtimeChanged + serviceChanged, err
}

func rewriteHostStorageVMRuntimePaths(serviceName string, components *db.VMComponentsConfig, mappings hostStoragePathMappings) (int, error) {
	if components == nil {
		return 0, nil
	}
	artifacts := []struct {
		name     string
		artifact *db.VMRuntimeArtifactConfig
	}{
		{name: "Configured", artifact: &components.Runtime.Configured},
		{name: "Staged", artifact: components.Runtime.Staged},
		{name: "Previous", artifact: components.Runtime.Previous},
	}
	var changed int
	for _, candidate := range artifacts {
		if candidate.artifact == nil {
			continue
		}
		artifactChanged, err := rewriteHostStorageVMFields(serviceName, mappings, []hostStorageRewriteField{
			{name: "VM.Components.Runtime." + candidate.name + ".Firecracker", value: &candidate.artifact.Firecracker},
			{name: "VM.Components.Runtime." + candidate.name + ".Jailer", value: &candidate.artifact.Jailer},
		})
		changed += artifactChanged
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}

type hostStorageRewriteField struct {
	name  string
	value *string
}

func rewriteHostStorageVMFields(serviceName string, mappings hostStoragePathMappings, fields []hostStorageRewriteField) (int, error) {
	var changed int
	for _, field := range fields {
		didChange, err := rewriteHostStoragePathValue(field.value, mappings)
		if err != nil {
			return changed, fmt.Errorf("rewrite service %q %s: %w", serviceName, field.name, err)
		}
		if didChange {
			changed++
		}
	}
	return changed, nil
}

func hostStorageServiceScopedRewriteMappings(mappings hostStoragePathMappings) hostStoragePathMappings {
	out := make(hostStoragePathMappings, 0, len(mappings))
	for _, mapping := range mappings {
		if mapping.Reason == hostStoragePathReasonDataDir {
			continue
		}
		out = append(out, mapping)
	}
	return out
}

func rewriteHostStorageArtifactRefs(serviceName string, artifacts db.ArtifactStore, mappings hostStoragePathMappings) (int, error) {
	artifactNames := make([]db.ArtifactName, 0, len(artifacts))
	for name := range artifacts {
		artifactNames = append(artifactNames, name)
	}
	slices.SortFunc(artifactNames, func(a, b db.ArtifactName) int {
		return strings.Compare(string(a), string(b))
	})
	var changed int
	for _, name := range artifactNames {
		artifact := artifacts[name]
		if artifact == nil {
			continue
		}
		refs := make([]db.ArtifactRef, 0, len(artifact.Refs))
		for ref := range artifact.Refs {
			refs = append(refs, ref)
		}
		slices.SortFunc(refs, func(a, b db.ArtifactRef) int {
			return strings.Compare(string(a), string(b))
		})
		for _, ref := range refs {
			value := artifact.Refs[ref]
			rewritten, didChange, err := mappings.Rewrite(value)
			if err != nil {
				return changed, fmt.Errorf("rewrite service %q Artifacts.%s.Refs.%s: %w", serviceName, name, ref, err)
			}
			if !didChange {
				continue
			}
			artifact.Refs[ref] = rewritten
			changed++
		}
	}
	return changed, nil
}

func rewriteHostStoragePathValue(value *string, mappings hostStoragePathMappings) (bool, error) {
	if value == nil {
		return false, nil
	}
	rewritten, changed, err := mappings.Rewrite(*value)
	if err != nil || !changed {
		return changed, err
	}
	*value = rewritten
	return true, nil
}
