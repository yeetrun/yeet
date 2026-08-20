// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/tui"
)

const outputTablePadding = 3

// renderOutputTable lays out plain cell content before adding terminal styles.
// text/tabwriter counts ANSI control bytes as visible characters, so styling
// cells before layout makes colored headings drift away from their columns.
func renderOutputTable(w io.Writer, headings []string, rows [][]string) error {
	if err := validateOutputTableRows(headings, rows); err != nil {
		return err
	}
	rendered, err := layoutOutputTable(headings, rows)
	if err != nil {
		return err
	}
	return writeOutputTable(w, rendered)
}

func validateOutputTableRows(headings []string, rows [][]string) error {
	for i, row := range rows {
		if len(row) != len(headings) {
			return fmt.Errorf("table row %d has %d columns, want %d", i, len(row), len(headings))
		}
	}
	return nil
}

func layoutOutputTable(headings []string, rows [][]string) (string, error) {
	var rendered bytes.Buffer
	tw := tabwriter.NewWriter(&rendered, 0, 0, outputTablePadding, ' ', 0)
	if err := writeOutputTableRow(tw, headings); err != nil {
		return "", err
	}
	for _, row := range rows {
		if err := writeOutputTableRow(tw, row); err != nil {
			return "", err
		}
	}
	if err := tw.Flush(); err != nil {
		return "", err
	}
	return rendered.String(), nil
}

func writeOutputTable(w io.Writer, rendered string) error {
	styles := outputStyles(w)
	for i, line := range strings.SplitAfter(rendered, "\n") {
		if line == "" {
			continue
		}
		if i == 0 && styles.Enabled() {
			line = styles.Render(tui.RoleHeading, strings.TrimSuffix(line, "\n")) + "\n"
		}
		if err := writeOutputTableLine(w, line); err != nil {
			return err
		}
	}
	return nil
}

func writeOutputTableRow(w io.Writer, cells []string) error {
	return writeOutputTableLine(w, strings.Join(cells, "\t")+"\n")
}

func writeOutputTableLine(w io.Writer, line string) error {
	n, err := io.WriteString(w, line)
	if err != nil {
		return err
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return nil
}
