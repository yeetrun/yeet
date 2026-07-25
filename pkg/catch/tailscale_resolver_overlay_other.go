//go:build !linux

// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "errors"

func mountTailscaleResolverOverlay(string, string) error {
	return errors.New("resolver overlays require Linux")
}
