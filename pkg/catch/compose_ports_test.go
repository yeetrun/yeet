// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUpdateComposePorts(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "compose.yml")
	compose := "services:\n  svc-a:\n    image: nginx:latest\n"
	if err := os.WriteFile(path, []byte(compose), 0644); err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	wantPorts := []string{"8000:80", "9000:90"}
	if err := updateComposePorts(path, "svc-a", wantPorts); err != nil {
		t.Fatalf("updateComposePorts returned error: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read updated compose: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(content, &doc); err != nil {
		t.Fatalf("failed to unmarshal updated compose: %v", err)
	}
	services := doc["services"].(map[string]any)
	service := services["svc-a"].(map[string]any)
	gotPorts, ok := service["ports"].([]any)
	if !ok {
		t.Fatalf("expected ports to be a list, got %T", service["ports"])
	}
	ports := make([]string, 0, len(gotPorts))
	for _, entry := range gotPorts {
		ports = append(ports, entry.(string))
	}
	if !reflect.DeepEqual(ports, wantPorts) {
		t.Fatalf("ports = %v, want %v", ports, wantPorts)
	}
}
