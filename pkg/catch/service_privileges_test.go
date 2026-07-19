// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestValidateNativeServicePrivilegedPorts(t *testing.T) {
	nonRoot := db.ServiceIdentity{UID: 1000, GID: 1000}
	root := db.ServiceIdentity{}
	for _, tt := range []struct {
		name     string
		ports    []string
		identity db.ServiceIdentity
		wantErr  string
	}{
		{name: "privileged wildcard", ports: []string{"80:8080"}, identity: nonRoot, wantErr: "privileged host port 80"},
		{name: "privileged address", ports: []string{"127.0.0.1:443:8443/tcp"}, identity: nonRoot, wantErr: "privileged host port 443"},
		{name: "unprivileged", ports: []string{"8080:8080"}, identity: nonRoot},
		{name: "container only", ports: []string{"53"}, identity: nonRoot},
		{name: "root bypass", ports: []string{"80:8080"}, identity: root},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNativeServicePrivilegedPorts("api", tt.ports, tt.identity)
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
