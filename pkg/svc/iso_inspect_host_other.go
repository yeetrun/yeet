//go:build !linux

// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

func isISONamespaceHostSource(string) (bool, error) {
	return false, nil
}
