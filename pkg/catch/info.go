// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"runtime"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

type ServerInfo struct {
	Version     string                  `json:"version"`
	GOOS        string                  `json:"goos"`
	GOARCH      string                  `json:"goarch"`
	InstallUser string                  `json:"installUser,omitempty"`
	InstallHost string                  `json:"installHost,omitempty"`
	RootDir     string                  `json:"rootDir,omitempty"`
	ServicesDir string                  `json:"servicesDir,omitempty"`
	ISO         catchrpc.ISOPoolSummary `json:"iso,omitzero"`
}

func GetInfo() ServerInfo {
	return ServerInfo{
		Version: Version(),
		GOARCH:  runtime.GOARCH,
		GOOS:    runtime.GOOS,
	}
}

func GetInfoWithInstallUser(installUser string, installHost string) ServerInfo {
	info := GetInfo()
	info.InstallUser = installUser
	info.InstallHost = installHost
	return info
}

func GetInfoWithConfig(cfg *Config) ServerInfo {
	info := GetInfoWithInstallUser(cfg.InstallUser, cfg.InstallHost)
	info.RootDir = cfg.RootDir
	info.ServicesDir = cfg.ServicesRoot
	if cfg.DB != nil {
		if dv, err := cfg.DB.Get(); err == nil {
			info.ISO = isoPoolSummary(dv.AsStruct())
		}
	}
	return info
}

func isoPoolSummary(data *db.Data) catchrpc.ISOPoolSummary {
	pool := configuredISOPool(data)
	if pool == nil {
		return catchrpc.ISOPoolSummary{}
	}
	summary := catchrpc.ISOPoolSummary{
		Prefix:    pool.Prefix.Masked().String(),
		Source:    pool.Source,
		Allocator: pool.AllocatorVersion,
		Policy:    pool.PolicyVersion,
		Conflict:  pool.LastConflict,
	}
	for _, service := range data.Services {
		if service == nil || service.ISO == nil {
			continue
		}
		addISOPoolAllocationSummary(&summary, service.ISO)
	}
	return summary
}

func configuredISOPool(data *db.Data) *db.ISOPool {
	if data == nil || data.ISOPool == nil || !data.ISOPool.Prefix.IsValid() {
		return nil
	}
	return data.ISOPool
}

func addISOPoolAllocationSummary(summary *catchrpc.ISOPoolSummary, allocation *db.ISOAllocation) {
	if allocation.Link.IsValid() {
		summary.LinksUsed++
	}
	if allocation.Project.IsValid() {
		summary.ProjectsUsed++
	}
	switch iso.AllocationState(allocation.State) {
	case iso.StateReserved:
		summary.Reserved++
	case iso.StateQuarantined:
		summary.Quarantined++
	case iso.StateTombstoned:
		summary.Tombstoned++
	case iso.StateReady, iso.StateStopped, iso.StateDegraded, iso.StateRemoving:
		summary.Active++
	}
}
