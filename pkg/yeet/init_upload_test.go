// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import "testing"

func TestFormatUploadDetail(t *testing.T) {
	tests := []struct {
		name  string
		sent  float64
		total float64
		rate  float64
		want  string
	}{
		{
			name:  "percent with rate and eta",
			sent:  512,
			total: 1024,
			rate:  256,
			want:  "50% 512.00 B/1024.00 B @ 256.00 B/s ETA 2s",
		},
		{
			name: "clamps negative sent",
			sent: -1,
			want: "0.00 B",
		},
		{
			name:  "clamps sent over total",
			sent:  2048,
			total: 1024,
			want:  "100% 1024.00 B/1024.00 B",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatUploadDetail(tt.sent, tt.total, tt.rate); got != tt.want {
				t.Fatalf("detail = %q, want %q", got, tt.want)
			}
		})
	}
}
