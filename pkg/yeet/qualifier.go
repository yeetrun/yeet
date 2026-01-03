// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import "strings"

func splitServiceHost(value string) (string, string, bool) {
	idx := strings.LastIndex(value, "@")
	if idx <= 0 || idx >= len(value)-1 {
		return value, "", false
	}
	return value[:idx], value[idx+1:], true
}
