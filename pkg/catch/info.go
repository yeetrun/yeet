// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "runtime"

type ServerInfo struct {
	Version     string `json:"version"`
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	InstallUser string `json:"installUser,omitempty"`
	InstallHost string `json:"installHost,omitempty"`
	RootDir     string `json:"rootDir,omitempty"`
	ServicesDir string `json:"servicesDir,omitempty"`
}

func GetInfo() ServerInfo {
	return ServerInfo{
		Version: VersionCommit(),
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
	return info
}
