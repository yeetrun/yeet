// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"testing"

	"github.com/shayne/yeet/pkg/db"
)

func TestServiceDataTypeForService(t *testing.T) {
	cases := []struct {
		name    string
		service *db.Service
		want    ServiceDataType
	}{
		{
			name: "systemd-cron",
			service: &db.Service{
				Name:        "svc-cron",
				ServiceType: db.ServiceTypeSystemd,
				Artifacts: db.ArtifactStore{
					db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/svc-cron.timer"}},
				},
			},
			want: ServiceDataTypeCron,
		},
		{
			name: "systemd-service",
			service: &db.Service{
				Name:        "svc-service",
				ServiceType: db.ServiceTypeSystemd,
			},
			want: ServiceDataTypeService,
		},
		{
			name: "docker-compose",
			service: &db.Service{
				Name:        "svc-docker",
				ServiceType: db.ServiceTypeDockerCompose,
			},
			want: ServiceDataTypeDocker,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ServiceDataTypeForService(tc.service.View())
			if got != tc.want {
				t.Fatalf("ServiceDataTypeForService() = %q, want %q", got, tc.want)
			}
		})
	}
}
