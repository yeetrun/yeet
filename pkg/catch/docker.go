// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/logtail/backoff"
)

var dockerComposeServiceStatus = map[string]ComponentStatus{
	"restart": "-", // Ignore restart events.
	"start":   ComponentStatusRunning,
	"kill":    ComponentStatusStopping,
	"oom":     ComponentStatusStopped,
	"die":     ComponentStatusStopped,
	"stop":    ComponentStatusStopped,
	"pause":   ComponentStatusStopped,
	"unpause": ComponentStatusRunning,

	// exec
	"exec_start":  "-",
	"exec_die":    "-",
	"exec_create": "-",
}

type dockerMonitorActor struct {
	ID         string            `json:"ID"`
	Attributes map[string]string `json:"Attributes"`
}

type dockerMonitorEvent struct {
	Status string             `json:"status"`
	ID     string             `json:"id"`
	Type   string             `json:"Type"`
	Action string             `json:"Action"`
	Actor  dockerMonitorActor `json:"Actor"`
}

func (s *Server) monitorDocker() {
	ctx := s.ctx
	// Create a backoff mechanism for retrying on errors
	bo := backoff.NewBackoff("docker-monitor", log.Printf, 60*time.Second)
execLoop:
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Get the Docker command
		docker, err := svc.DockerCmd()
		if err != nil {
			log.Printf("failed to get docker command: %v", err)
			bo.BackOff(ctx, err)
			continue
		}
		bo.BackOff(ctx, nil) // reset backoff on success

		// Start Docker events monitoring
		cmd := exec.CommandContext(ctx, docker, "events", "--format=json")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("failed to get stdout pipe: %v", err)
			continue
		}
		if err := cmd.Start(); err != nil {
			log.Printf("failed to run docker ps: %v", err)
			continue
		}

		// Create a JSON decoder for reading Docker events
		je := json.NewDecoder(stdout)
		for {
			var entry dockerMonitorEvent
			// Decode the next event
			if err := je.Decode(&entry); err != nil {
				if errors.Is(err, io.EOF) {
					continue execLoop
				}
				log.Printf("failed to unmarshal docker event: %v", err)
				continue
			}

			s.handleDockerMonitorEvent(entry)
		}
	}
}

func (s *Server) handleDockerMonitorEvent(entry dockerMonitorEvent) bool {
	sn, cn, ok := s.dockerMonitorServiceComponent(entry)
	if !ok {
		return false
	}

	if entry.Action == "destroy" {
		s.removeDockerComponentStatus(sn, cn)
		log.Printf("docker event: %s %s %s", sn, cn, entry.Action)
		return false
	}

	st, ok := dockerMonitorComponentStatus(entry)
	if !ok {
		return false
	}

	data := s.updateDockerComponentStatus(sn, cn, st)
	log.Printf("docker event: %s %s %s", sn, cn, entry.Action)
	s.PublishEvent(Event{
		Type:        EventTypeServiceStatusChanged,
		ServiceName: sn,
		Data:        EventData{Data: data},
	})
	return true
}

func (s *Server) dockerMonitorServiceComponent(entry dockerMonitorEvent) (string, string, bool) {
	if entry.Type != "container" {
		return "", "", false
	}
	sn, ok := dockerMonitorServiceName(entry.Actor.Attributes)
	if !ok {
		return "", "", false
	}
	if _, err := s.serviceView(sn); err != nil {
		if !errors.Is(err, errServiceNotFound) {
			log.Printf("failed to get service view: %v", err)
		}
		return "", "", false
	}
	cn, ok := entry.Actor.Attributes["com.docker.compose.service"]
	if !ok {
		return "", "", false
	}
	return sn, cn, true
}

func dockerMonitorServiceName(attrs map[string]string) (string, bool) {
	pn, ok := attrs["com.docker.compose.project"]
	if !ok {
		return "", false
	}
	return strings.CutPrefix(pn, "catch-")
}

func dockerMonitorComponentStatus(entry dockerMonitorEvent) (ComponentStatus, bool) {
	st, ok := dockerComposeServiceStatus[entry.Action]
	if !ok {
		action, _, cut := strings.Cut(entry.Action, ":")
		if !cut {
			log.Printf("container %q unknown action: %v", entry.ID, entry.Action)
			return "", false
		}
		st, ok = dockerComposeServiceStatus[action]
		if !ok {
			log.Printf("container %q unknown action: %v", entry.ID, entry.Action)
			return "", false
		}
	}
	if st == "-" {
		return "", false
	}
	return st, true
}

func (s *Server) removeDockerComponentStatus(sn, cn string) {
	s.serviceStatus.mu.Lock()
	defer s.serviceStatus.mu.Unlock()
	delete(s.serviceStatus.m[sn], cn)
	if len(s.serviceStatus.m[sn]) == 0 {
		delete(s.serviceStatus.m, sn)
	}
}

func (s *Server) updateDockerComponentStatus(sn, cn string, st ComponentStatus) ServiceStatusData {
	s.serviceStatus.mu.Lock()
	defer s.serviceStatus.mu.Unlock()
	if s.serviceStatus.m == nil {
		s.serviceStatus.m = make(map[string]map[string]ComponentStatus)
	}
	if _, ok := s.serviceStatus.m[sn]; !ok {
		s.serviceStatus.m[sn] = make(map[string]ComponentStatus)
	}
	s.serviceStatus.m[sn][cn] = st
	data := ServiceStatusData{
		ServiceName: sn,
		ServiceType: ServiceDataTypeDocker,
	}
	for cn, st := range s.serviceStatus.m[sn] {
		data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
			Name:   cn,
			Status: st,
		})
	}
	return data
}
