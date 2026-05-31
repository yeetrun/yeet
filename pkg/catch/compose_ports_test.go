// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

	wantPorts := []string{"8000:80", "9000:90", "5353:5353/udp"}
	if err := updateComposePorts(path, "svc-a", []string{" 8000:80/tcp ", "", "9000:90/TCP", "5353:5353/UDP", "  "}); err != nil {
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

func TestUpdateComposePortsEmptyPublishRemovesPorts(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "compose.yml")
	compose := "services:\n  svc-a:\n    image: nginx:latest\n    ports:\n      - 8000:80\n      - 9000:90\n"
	if err := os.WriteFile(path, []byte(compose), 0644); err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	if err := updateComposePorts(path, "svc-a", []string{" ", ""}); err != nil {
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
	if _, ok := service["ports"]; ok {
		t.Fatalf("ports key still present after empty publish: %#v", service["ports"])
	}
	if got := service["image"]; got != "nginx:latest" {
		t.Fatalf("image = %v, want nginx:latest", got)
	}
}

func TestReadComposePorts(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "compose.yml")
	compose := "services:\n  svc-a:\n    image: nginx:latest\n    ports:\n      - 8000:80/tcp\n      - 127.0.0.1:9000:90/TCP\n      - 5353:5353/udp\n  svc-b:\n    image: caddy:latest\n"
	if err := os.WriteFile(path, []byte(compose), 0644); err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	ports, err := readComposePorts(path, "svc-a")
	if err != nil {
		t.Fatalf("readComposePorts returned error: %v", err)
	}
	wantPorts := []string{"8000:80", "127.0.0.1:9000:90", "5353:5353/udp"}
	if !reflect.DeepEqual(ports, wantPorts) {
		t.Fatalf("ports = %v, want %v", ports, wantPorts)
	}

	ports, err = readComposePorts(path, "svc-b")
	if err != nil {
		t.Fatalf("readComposePorts for service without ports returned error: %v", err)
	}
	if len(ports) != 0 {
		t.Fatalf("ports for service without ports = %v, want empty", ports)
	}
}

func TestReadComposePortsErrorsForMissingOrMalformedService(t *testing.T) {
	tmp := t.TempDir()

	missingServicePath := filepath.Join(tmp, "missing-service.yml")
	if err := os.WriteFile(missingServicePath, []byte("services:\n  svc-a:\n    image: nginx:latest\n"), 0644); err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}
	if _, err := readComposePorts(missingServicePath, "missing"); err == nil || !strings.Contains(err.Error(), `compose service "missing" not found`) {
		t.Fatalf("readComposePorts missing service error = %v, want service not found", err)
	}

	malformedServicePath := filepath.Join(tmp, "malformed-service.yml")
	if err := os.WriteFile(malformedServicePath, []byte("services:\n  svc-a: nginx:latest\n"), 0644); err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}
	if _, err := readComposePorts(malformedServicePath, "svc-a"); err == nil || !strings.Contains(err.Error(), `compose service "svc-a" is malformed`) {
		t.Fatalf("readComposePorts malformed service error = %v, want malformed service", err)
	}

	malformedPortsPath := filepath.Join(tmp, "malformed-ports.yml")
	if err := os.WriteFile(malformedPortsPath, []byte("services:\n  svc-a:\n    image: nginx:latest\n    ports: 8000:80\n"), 0644); err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}
	if _, err := readComposePorts(malformedPortsPath, "svc-a"); err == nil || !strings.Contains(err.Error(), `compose service "svc-a" ports are malformed`) {
		t.Fatalf("readComposePorts malformed ports error = %v, want malformed ports", err)
	}
}
