// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestDockerMonitorEventPublishesStatusTransition(t *testing.T) {
	server := newTestServer(t)
	addTestService(t, server, "web", db.ServiceTypeDockerCompose)
	events := make(chan Event, 1)
	handle := server.AddEventListener(events, nil)
	defer server.RemoveEventListener(handle)

	published := server.handleDockerMonitorEvent(dockerMonitorEvent{
		Type:   "container",
		Action: "start",
		Actor: dockerMonitorActor{
			Attributes: map[string]string{
				"com.docker.compose.project": "catch-web",
				"com.docker.compose.service": "app",
			},
		},
	})

	if !published {
		t.Fatalf("expected event to be published")
	}
	event := <-events
	if event.Type != EventTypeServiceStatusChanged || event.ServiceName != "web" {
		t.Fatalf("unexpected event: %#v", event)
	}
	data, ok := event.Data.Data.(ServiceStatusData)
	if !ok {
		t.Fatalf("unexpected event data type: %T", event.Data.Data)
	}
	if data.ServiceName != "web" || data.ServiceType != ServiceDataTypeDocker {
		t.Fatalf("unexpected status data: %#v", data)
	}
	assertComponentStatus(t, data.ComponentStatus, "app", ComponentStatusRunning)
	assertStoredComponentStatus(t, server, "web", "app", ComponentStatusRunning)
}

func TestDockerMonitorEventHandlesColonActionAndDestroy(t *testing.T) {
	server := newTestServer(t)
	addTestService(t, server, "web", db.ServiceTypeDockerCompose)

	published := server.handleDockerMonitorEvent(dockerMonitorEvent{
		Type:   "container",
		Action: "die: signal",
		Actor: dockerMonitorActor{
			Attributes: map[string]string{
				"com.docker.compose.project": "catch-web",
				"com.docker.compose.service": "app",
			},
		},
	})
	if !published {
		t.Fatalf("expected colon action to publish")
	}
	assertStoredComponentStatus(t, server, "web", "app", ComponentStatusStopped)

	published = server.handleDockerMonitorEvent(dockerMonitorEvent{
		Type:   "container",
		Action: "destroy",
		Actor: dockerMonitorActor{
			Attributes: map[string]string{
				"com.docker.compose.project": "catch-web",
				"com.docker.compose.service": "app",
			},
		},
	})
	if published {
		t.Fatalf("destroy should update state without publishing")
	}
	server.serviceStatus.mu.Lock()
	defer server.serviceStatus.mu.Unlock()
	if _, ok := server.serviceStatus.m["web"]; ok {
		t.Fatalf("expected service status to be removed after destroy: %#v", server.serviceStatus.m)
	}
}

func TestDockerMonitorEventFiltersUntrackedEvents(t *testing.T) {
	server := newTestServer(t)
	addTestService(t, server, "web", db.ServiceTypeDockerCompose)

	tests := []struct {
		name  string
		event dockerMonitorEvent
	}{
		{
			name: "non-container",
			event: dockerMonitorEvent{
				Type:   "network",
				Action: "start",
			},
		},
		{
			name: "non-catch project",
			event: dockerMonitorEvent{
				Type:   "container",
				Action: "start",
				Actor: dockerMonitorActor{Attributes: map[string]string{
					"com.docker.compose.project": "other-web",
					"com.docker.compose.service": "app",
				}},
			},
		},
		{
			name: "missing service label",
			event: dockerMonitorEvent{
				Type:   "container",
				Action: "start",
				Actor: dockerMonitorActor{Attributes: map[string]string{
					"com.docker.compose.project": "catch-web",
				}},
			},
		},
		{
			name: "ignored action",
			event: dockerMonitorEvent{
				Type:   "container",
				Action: "exec_start",
				Actor: dockerMonitorActor{Attributes: map[string]string{
					"com.docker.compose.project": "catch-web",
					"com.docker.compose.service": "app",
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if server.handleDockerMonitorEvent(tt.event) {
				t.Fatalf("expected event to be filtered")
			}
		})
	}
}

func TestSystemdMonitorEntryPublishesStatusTransition(t *testing.T) {
	server := newTestServer(t)
	addTestService(t, server, "worker", db.ServiceTypeSystemd)
	events := make(chan Event, 1)
	handle := server.AddEventListener(events, nil)
	defer server.RemoveEventListener(handle)

	published := server.handleSystemdMonitorEntry(systemdMonitorEntry{
		Unit:      "worker.service",
		MessageID: "39f53479d3a045ac8e11786248231fbf",
	})

	if !published {
		t.Fatalf("expected event to be published")
	}
	event := <-events
	if event.Type != EventTypeServiceStatusChanged || event.ServiceName != "worker" {
		t.Fatalf("unexpected event: %#v", event)
	}
	data, ok := event.Data.Data.(ServiceStatusData)
	if !ok {
		t.Fatalf("unexpected event data type: %T", event.Data.Data)
	}
	if data.ServiceName != "worker" || data.ServiceType != ServiceDataTypeService {
		t.Fatalf("unexpected status data: %#v", data)
	}
	assertComponentStatus(t, data.ComponentStatus, "worker", ComponentStatusRunning)
	assertStoredComponentStatus(t, server, "worker", "worker", ComponentStatusRunning)
}

func TestSystemdMonitorEntryFiltersIgnoredEntries(t *testing.T) {
	server := newTestServer(t)
	addTestService(t, server, "worker", db.ServiceTypeSystemd)

	tests := []struct {
		name  string
		entry systemdMonitorEntry
	}{
		{
			name:  "missing message id",
			entry: systemdMonitorEntry{Unit: "worker.service"},
		},
		{
			name: "ignored message id",
			entry: systemdMonitorEntry{
				Unit:      "worker.service",
				MessageID: "98e322203f7a4ed290d09fe03c09fe15",
			},
		},
		{
			name: "non-service unit",
			entry: systemdMonitorEntry{
				Unit:      "worker.timer",
				MessageID: "39f53479d3a045ac8e11786248231fbf",
			},
		},
		{
			name: "unknown service",
			entry: systemdMonitorEntry{
				Unit:      "missing.service",
				MessageID: "39f53479d3a045ac8e11786248231fbf",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if server.handleSystemdMonitorEntry(tt.entry) {
				t.Fatalf("expected entry to be filtered")
			}
		})
	}
}

func addTestService(t *testing.T, server *Server, name string, serviceType db.ServiceType) {
	t.Helper()
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, service *db.Service) error {
		service.Name = name
		service.ServiceType = serviceType
		return nil
	}); err != nil {
		t.Fatalf("MutateService(%q): %v", name, err)
	}
}

func assertComponentStatus(t *testing.T, components []ComponentStatusData, name string, want ComponentStatus) {
	t.Helper()
	for _, component := range components {
		if component.Name == name {
			if component.Status != want {
				t.Fatalf("component %q status = %q, want %q", name, component.Status, want)
			}
			return
		}
	}
	t.Fatalf("missing component %q in %#v", name, components)
}

func assertStoredComponentStatus(t *testing.T, server *Server, serviceName, componentName string, want ComponentStatus) {
	t.Helper()
	server.serviceStatus.mu.Lock()
	defer server.serviceStatus.mu.Unlock()
	got, ok := server.serviceStatus.m[serviceName][componentName]
	if !ok {
		t.Fatalf("missing stored status for %s/%s: %#v", serviceName, componentName, server.serviceStatus.m)
	}
	if got != want {
		t.Fatalf("stored status for %s/%s = %q, want %q", serviceName, componentName, got, want)
	}
}
