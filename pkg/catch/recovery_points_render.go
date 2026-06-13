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
	"time"
)

func renderRecoveryPoints(w io.Writer, formatOut string, points []recoveryPoint) error {
	switch strings.TrimSpace(formatOut) {
	case "json":
		return json.NewEncoder(w).Encode(points)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(points)
	default:
		return renderRecoveryPointsTable(w, points)
	}
}

func renderRecoveryPointsTable(w io.Writer, points []recoveryPoint) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SERVICE\tTYPE\tSNAPSHOT\tCREATED\tMODE\tEVENT\tRETENTION\tCOMMENT"); err != nil {
		return err
	}
	for _, point := range points {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			point.Service,
			point.ServiceType,
			point.ShortName,
			formatRecoveryPointCreated(point.Created),
			point.Mode,
			point.Event,
			formatRecoveryPointRetention(point),
			strings.TrimSpace(point.Comment),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func renderRecoveryPointInspect(w io.Writer, formatOut string, point recoveryPoint) error {
	switch strings.TrimSpace(formatOut) {
	case "json":
		return json.NewEncoder(w).Encode(point)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(point)
	default:
		return renderRecoveryPointInspectText(w, point)
	}
}

func renderRecoveryPointInspectText(w io.Writer, point recoveryPoint) error {
	type inspectLine struct {
		label string
		value string
	}
	lines := []inspectLine{
		{"Service", point.Service},
		{"Type", point.ServiceType},
		{"Snapshot", point.Name},
		{"Short name", point.ShortName},
		{"Created", formatRecoveryPointCreated(point.Created)},
		{"Mode", point.Mode},
		{"Event", point.Event},
		{"Retention", formatRecoveryPointRetention(point)},
	}
	if strings.TrimSpace(point.Comment) != "" {
		lines = append(lines, inspectLine{"Comment", strings.TrimSpace(point.Comment)})
	}
	if strings.TrimSpace(point.StatePath) != "" {
		lines = append(lines,
			inspectLine{"Firecracker state", point.StatePath},
			inspectLine{"Firecracker memory", point.MemoryPath},
		)
	}
	lines = append(lines, inspectLine{"Actions", strings.Join(point.Actions, ", ")})

	for _, line := range lines {
		if _, err := fmt.Fprintf(w, "%s: %s\n", line.label, line.value); err != nil {
			return err
		}
	}
	return nil
}

func formatRecoveryPointCreated(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func formatRecoveryPointRetention(point recoveryPoint) string {
	if strings.TrimSpace(point.Retention) != "" {
		return point.Retention
	}
	return recoveryRetentionLabel(point.Protected)
}
