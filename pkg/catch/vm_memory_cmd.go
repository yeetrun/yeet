// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/db"
)

type vmMemoryStatus struct {
	Policy            string `json:"policy"`
	HostBytes         int64  `json:"hostBytes"`
	MemAvailableBytes int64  `json:"memAvailableBytes"`
	ReserveBytes      int64  `json:"reserveBytes"`
	BudgetBytes       int64  `json:"budgetBytes"`
}

func (s *Server) setVMMemoryPolicy(policyName string) error {
	if strings.TrimSpace(policyName) == "" {
		return fmt.Errorf("vm memory set requires --policy=safe|balanced|aggressive")
	}
	policy, err := normalizeVMHostMemoryPolicy(policyName)
	if err != nil {
		return err
	}
	_, err = s.cfg.DB.MutateData(func(d *db.Data) error {
		if d.VMHost == nil {
			d.VMHost = &db.VMHostConfig{}
		}
		d.VMHost.MemoryPolicy = policy.Name
		return nil
	})
	return err
}

func (s *Server) printVMMemoryStatus(w io.Writer, formatOut string) error {
	status, err := s.vmMemoryStatus()
	if err != nil {
		return err
	}
	switch strings.TrimSpace(formatOut) {
	case "", "table":
		return renderVMMemoryStatusTable(w, status)
	case "json":
		return json.NewEncoder(w).Encode(status)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(status)
	default:
		return fmt.Errorf("unsupported vm memory format %q", formatOut)
	}
}

func (s *Server) vmMemoryStatus() (vmMemoryStatus, error) {
	policy, err := s.vmHostMemoryPolicy()
	if err != nil {
		return vmMemoryStatus{}, err
	}
	hostBytes := linuxMemTotalBytes()
	availableBytes := linuxMemAvailableBytes()
	return vmMemoryStatus{
		Policy:            policy.Name,
		HostBytes:         hostBytes,
		MemAvailableBytes: availableBytes,
		ReserveBytes:      vmHostMemoryReserve(hostBytes),
		BudgetBytes:       vmHostMemoryBudget(hostBytes),
	}, nil
}

func renderVMMemoryStatusTable(w io.Writer, status vmMemoryStatus) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "POLICY\tHOST\tAVAILABLE\tRESERVE\tBUDGET"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
		status.Policy,
		formatBytesInt(status.HostBytes),
		formatBytesInt(status.MemAvailableBytes),
		formatBytesInt(status.ReserveBytes),
		formatBytesInt(status.BudgetBytes),
	); err != nil {
		return err
	}
	return tw.Flush()
}
