// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iso

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateNetworkMatrix(t *testing.T) {
	tests := []struct {
		name    string
		req     NetworkRequest
		wantErr string
	}{
		{name: "vm", req: NetworkRequest{Payload: PayloadVM, Modes: []string{"iso"}}},
		{name: "compose tailscale", req: NetworkRequest{Payload: PayloadCompose, Modes: []string{"iso", "ts"}}},
		{name: "svc conflict", req: NetworkRequest{Payload: PayloadCompose, Modes: []string{"iso", "svc"}}, wantErr: "cannot combine"},
		{name: "lan conflict", req: NetworkRequest{Payload: PayloadContainer, Modes: []string{"iso", "lan"}}, wantErr: "cannot combine"},
		{name: "vm tailscale", req: NetworkRequest{Payload: PayloadVM, Modes: []string{"iso", "ts"}}, wantErr: "VMs support only iso"},
		{name: "native", req: NetworkRequest{Payload: PayloadNative, Modes: []string{"iso"}}, wantErr: "native"},
		{name: "cron", req: NetworkRequest{Payload: PayloadCron, Modes: []string{"iso"}}, wantErr: "cron"},
		{name: "publish", req: NetworkRequest{Payload: PayloadContainer, Modes: []string{"iso"}, Published: true}, wantErr: "published ports"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNetwork(tt.req)
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateNetworkPublishedPortsError(t *testing.T) {
	err := ValidateNetwork(NetworkRequest{Payload: PayloadContainer, Modes: []string{"iso"}, Published: true})
	if !errors.Is(err, ErrPublishedPorts) {
		t.Fatalf("ValidateNetwork error = %v, want ErrPublishedPorts", err)
	}
}
