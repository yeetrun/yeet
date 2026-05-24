// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

func getService() string {
	if serviceOverride != "" {
		return serviceOverride
	}
	return systemServiceName
}

func SystemServiceName() string {
	return systemServiceName
}
