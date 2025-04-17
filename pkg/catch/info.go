// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "runtime"

type ServerInfo struct {
	Version string `json:"version"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
}

func GetInfo() ServerInfo {
	return ServerInfo{
		Version: VersionCommit(),
		GOARCH:  runtime.GOARCH,
		GOOS:    runtime.GOOS,
	}
}
