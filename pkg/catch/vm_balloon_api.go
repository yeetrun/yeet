// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

const firecrackerBalloonPageSize = int64(4096)

type vmBalloonAPI interface {
	SetTarget(context.Context, string, int64) error
	Stats(context.Context, string) (vmBalloonStats, error)
}

type vmBalloonStats struct {
	TargetBytes          int64
	ActualBytes          int64
	FreeMemoryBytes      int64
	AvailableMemoryBytes int64
}

type firecrackerBalloonAPI struct{}

var _ vmBalloonAPI = firecrackerBalloonAPI{}

type firecrackerBalloonTargetRequest struct {
	AmountMib int64 `json:"amount_mib"`
}

type firecrackerBalloonStatsResponse struct {
	TargetPages     int64 `json:"target_pages"`
	ActualPages     int64 `json:"actual_pages"`
	FreeMemory      int64 `json:"free_memory"`
	AvailableMemory int64 `json:"available_memory"`
}

func (firecrackerBalloonAPI) SetTarget(ctx context.Context, socket string, targetBytes int64) error {
	if targetBytes < 0 {
		return fmt.Errorf("firecracker balloon target must not be negative")
	}
	body := firecrackerBalloonTargetRequest{AmountMib: targetBytes >> 20}
	return firecrackerBalloonJSON(ctx, socket, http.MethodPatch, "http://unix/balloon", body, nil)
}

func (firecrackerBalloonAPI) Stats(ctx context.Context, socket string) (vmBalloonStats, error) {
	var raw firecrackerBalloonStatsResponse
	if err := firecrackerBalloonJSON(ctx, socket, http.MethodGet, "http://unix/balloon/statistics", nil, &raw); err != nil {
		return vmBalloonStats{}, err
	}
	return vmBalloonStats{
		TargetBytes:          raw.TargetPages * firecrackerBalloonPageSize,
		ActualBytes:          raw.ActualPages * firecrackerBalloonPageSize,
		FreeMemoryBytes:      raw.FreeMemory,
		AvailableMemoryBytes: raw.AvailableMemory,
	}, nil
}

func firecrackerBalloonJSON(ctx context.Context, socket string, method string, url string, body any, out any) error {
	req, err := firecrackerBalloonRequest(ctx, socket, method, url, body)
	if err != nil {
		return err
	}
	resp, err := firecrackerUnixHTTPClient(socket).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return firecrackerBalloonStatusError(resp, method, url)
	}
	return decodeFirecrackerBalloonResponse(resp.Body, method, url, out)
}

func firecrackerBalloonRequest(ctx context.Context, socket string, method string, url string, body any) (*http.Request, error) {
	if strings.TrimSpace(socket) == "" {
		return nil, fmt.Errorf("firecracker balloon API socket path is empty")
	}
	reader, hasBody, err := firecrackerBalloonRequestBody(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func firecrackerBalloonRequestBody(body any) (io.Reader, bool, error) {
	if body == nil {
		return nil, false, nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, false, err
	}
	return bytes.NewReader(raw), true, nil
}

func firecrackerUnixHTTPClient(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
}

func firecrackerBalloonStatusError(resp *http.Response, method string, url string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	bodyText := strings.TrimSpace(string(raw))
	if bodyText != "" {
		return fmt.Errorf("firecracker balloon API %s %s returned %s: %s", method, url, resp.Status, bodyText)
	}
	return fmt.Errorf("firecracker balloon API %s %s returned %s", method, url, resp.Status)
}

func decodeFirecrackerBalloonResponse(body io.Reader, method string, url string, out any) error {
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("decode firecracker balloon API %s %s response: %w", method, url, err)
	}
	return nil
}
