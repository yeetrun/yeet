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
			// Define the structure for Docker events
			var entry struct {
				Status string `json:"status"`
				ID     string `json:"id"`
				Type   string `json:"Type"`
				Action string `json:"Action"`
				Actor  struct {
					ID         string            `json:"ID"`
					Attributes map[string]string `json:"Attributes"`
				} `json:"Actor"`
			}

			// Decode the next event
			if err := je.Decode(&entry); err != nil {
				if errors.Is(err, io.EOF) {
					continue execLoop
				}
				log.Printf("failed to unmarshal docker event: %v", err)
				continue
			}

			// Skip non-container events
			if entry.Type != "container" {
				continue
			}

			// Extract the service name from the Docker Compose project name
			var sn string
			if pn, ok := entry.Actor.Attributes["com.docker.compose.project"]; !ok {
				continue
			} else if s, ok := strings.CutPrefix(pn, "catch-"); !ok {
				continue
			} else {
				sn = s
			}

			// Verify the service exists
			if _, err := s.serviceView(sn); err != nil {
				if errors.Is(err, errServiceNotFound) {
					continue
				}
				log.Printf("failed to get service view: %v", err)
				continue
			}

			// Get the Docker Compose service name
			cn, ok := entry.Actor.Attributes["com.docker.compose.service"]
			if !ok {
				// No compose service.
				continue
			}

			// Prepare the service status data
			data := ServiceStatusData{
				ServiceName: sn,
				ServiceType: ServiceDataTypeDocker,
			}

			// Handle container destruction
			if entry.Action == "destroy" {
				s.serviceStatus.mu.Lock()
				delete(s.serviceStatus.m[sn], cn)
				if len(s.serviceStatus.m[sn]) == 0 {
					delete(s.serviceStatus.m, sn)
				}
				s.serviceStatus.mu.Unlock()
				log.Printf("docker event: %s %s %s", sn, cn, entry.Action)
			} else {
				// Handle other container actions
				st, ok := dockerComposeServiceStatus[entry.Action]
				if !ok {
					// The action can also be of the form "<action>:...".
					action, _, ok := strings.Cut(entry.Action, ":")
					if !ok {
						log.Printf("container %q unknown action: %v", entry.ID, entry.Action)
						continue
					}
					st, ok = dockerComposeServiceStatus[action]
					if !ok {
						log.Printf("container %q unknown action: %v", entry.ID, entry.Action)
						continue
					}
				}
				if st == "-" {
					continue
				}

				// Update the service status
				s.serviceStatus.mu.Lock()
				if s.serviceStatus.m == nil {
					s.serviceStatus.m = make(map[string]map[string]ComponentStatus)
				}
				if _, ok := s.serviceStatus.m[sn]; !ok {
					s.serviceStatus.m[sn] = make(map[string]ComponentStatus)
				}
				s.serviceStatus.m[sn][cn] = st
				for cn, st := range s.serviceStatus.m[sn] {
					data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
						Name:   cn,
						Status: st,
					})
				}
				s.serviceStatus.mu.Unlock()
				log.Printf("docker event: %s %s %s", sn, cn, entry.Action)
				// Publish the service status change event
				s.PublishEvent(Event{
					Type:        EventTypeServiceStatusChanged,
					ServiceName: sn,
					Data:        EventData{Data: data},
				})
			}
		}
	}
}
