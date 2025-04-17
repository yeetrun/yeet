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
)

var systemdMessageIDs = map[string]ComponentStatus{
	// From https://github.com/systemd/systemd-stable/blob/main/catalog/systemd.catalog.in
	"7d4958e842da4a758f6c1cdc7b36dcc5": ComponentStatusStarting,
	"39f53479d3a045ac8e11786248231fbf": ComponentStatusRunning,
	"7ad2d189f7e94e70a38c781354912448": ComponentStatusStopped,
	"de5b426a63be47a7b6ac3eaac82e2f6f": ComponentStatusStopping,

	"5eb03494b6584870a536b337290809b3": "-", // restart scheduled
	"98e322203f7a4ed290d09fe03c09fe15": "-", // exited
	"d9b373ed55a64feb8242e02dbe79a49c": "-", // unit failed
	"be02cf6855d2428ba40df7e9d022f03d": "-", // start job failed

	// ignore
	"d34d037fff1847e6ae669a370e694725": "-", // Reloading
	"7b05ebc668384222baa8881179cfda54": "-", // Reloaded
	"9d1aaa27d60140bd96365438aad20286": "-", // "stop" job finished
	"c772d24e9a884cbeb9ea12625c306c01": "-", // Config error
}

func (s *Server) monitorSystemd() {
	ctx := s.ctx
execLoop:
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		cmd := exec.CommandContext(ctx, "journalctl", "--follow", "-o", "json", "_PID=1")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("failed to get stdout pipe: %v", err)
			continue
		}
		if err := cmd.Start(); err != nil {
			log.Printf("failed to run journalctl: %v", err)
			continue
		}
		// Read the output until the context is done.
		je := json.NewDecoder(stdout)
		for {
			var entry struct {
				Unit      string `json:"UNIT"`
				MessageID string `json:"MESSAGE_ID"`
			}
			if err := je.Decode(&entry); err != nil {
				if errors.Is(err, io.EOF) {
					continue execLoop
				}
				log.Printf("failed to unmarshal journal entry: %v", err)
				continue
			}
			if entry.MessageID == "" {
				continue
			}
			status, ok := systemdMessageIDs[entry.MessageID]
			if !ok {
				log.Printf("unknown systemd message id: %+v", entry)
				continue
			} else if status == "-" {
				continue
			}
			sn, ok := strings.CutSuffix(entry.Unit, ".service")
			if !ok {
				continue
			}
			if _, err := s.serviceView(sn); err != nil {
				if errors.Is(err, errServiceNotFound) {
					continue
				}
				log.Printf("failed to get service view: %v", err)
				continue
			}

			s.serviceStatus.mu.Lock()
			if s.serviceStatus.m == nil {
				s.serviceStatus.m = make(map[string]map[string]ComponentStatus)
			}
			if _, ok := s.serviceStatus.m[sn]; !ok {
				s.serviceStatus.m[sn] = make(map[string]ComponentStatus)
			}
			s.serviceStatus.m[sn][sn] = status
			s.serviceStatus.mu.Unlock()
			log.Printf("Service %q status: %v", entry.Unit, status)

			data := ServiceStatusData{
				ServiceName: sn,
				ServiceType: ServiceDataTypeService,
				ComponentStatus: []ComponentStatusData{
					{
						Name:   sn,
						Status: status,
					},
				},
			}
			s.PublishEvent(Event{
				Type:        EventTypeServiceStatusChanged,
				ServiceName: sn,
				Data:        EventData{Data: data},
			})
		}
	}
}
