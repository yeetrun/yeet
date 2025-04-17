// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build tools

// This file exists just so `go mod tidy` won't remove
// tool modules from our go.mod.
package tools

import (
	_ "github.com/google/addlicense"
	_ "tailscale.com/cmd/cloner"
)
