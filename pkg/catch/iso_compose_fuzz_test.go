// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "testing"

func FuzzAdmitISOCompose(f *testing.F) {
	f.Add([]byte(`{"name":"catch-app","networks":{"default":{"name":"catch-app_default","ipam":{}}},"services":{"api":{"image":"nginx","networks":{"default":null}}}}`))
	f.Add([]byte(`{"name":"catch-app","networks":{"default":{"name":"catch-app_default","ipam":{}}},"services":{"api":{"network_mode":"host","networks":{"default":null}}}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = AdmitISOCompose(raw, ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: t.TempDir(), MaxComponents: 29})
	})
}
