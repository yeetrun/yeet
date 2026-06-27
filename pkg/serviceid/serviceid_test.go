// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package serviceid

import (
	"strings"
	"testing"
)

func TestValidateAcceptsPortableServiceNames(t *testing.T) {
	for _, name := range []string{
		"api",
		"svc-a",
		"a1",
		"web-01",
		strings.Repeat("a", MaxLength),
	} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(name); err != nil {
				t.Fatalf("Validate(%q) returned error: %v", name, err)
			}
		})
	}
}

func TestValidateRejectsUnsafeServiceNames(t *testing.T) {
	tests := []struct {
		name    string
		service string
	}{
		{name: "empty", service: ""},
		{name: "dot", service: "bad.name"},
		{name: "leading number", service: "1svc"},
		{name: "leading dash", service: "-svc"},
		{name: "trailing dash", service: "svc-"},
		{name: "underscore", service: "bad_name"},
		{name: "uppercase", service: "Bad"},
		{name: "space", service: "bad name"},
		{name: "slash", service: "bad/name"},
		{name: "too long", service: strings.Repeat("a", MaxLength+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(tt.service); err == nil {
				t.Fatalf("Validate(%q) error = nil, want invalid", tt.service)
			}
		})
	}
}
