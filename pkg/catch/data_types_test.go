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
			name: "systemd-binary",
			service: &db.Service{
				Name:        "svc-binary",
				ServiceType: db.ServiceTypeSystemd,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/svc-binary"}},
				},
			},
			want: ServiceDataTypeBinary,
		},
		{
			name: "systemd-typescript",
			service: &db.Service{
				Name:        "svc-ts",
				ServiceType: db.ServiceTypeSystemd,
				Artifacts: db.ArtifactStore{
					db.ArtifactTypeScriptFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/svc-ts.ts"}},
				},
			},
			want: ServiceDataTypeTypeScript,
		},
		{
			name: "docker-compose-typescript",
			service: &db.Service{
				Name:        "svc-ts",
				ServiceType: db.ServiceTypeDockerCompose,
				Artifacts: db.ArtifactStore{
					db.ArtifactTypeScriptFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/svc-ts.ts"}},
				},
			},
			want: ServiceDataTypeTypeScript,
		},
		{
			name: "systemd-python",
			service: &db.Service{
				Name:        "svc-py",
				ServiceType: db.ServiceTypeSystemd,
				Artifacts: db.ArtifactStore{
					db.ArtifactPythonFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/svc-py.py"}},
				},
			},
			want: ServiceDataTypePython,
		},
		{
			name: "docker-compose-python",
			service: &db.Service{
				Name:        "svc-py",
				ServiceType: db.ServiceTypeDockerCompose,
				Artifacts: db.ArtifactStore{
					db.ArtifactPythonFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/svc-py.py"}},
				},
			},
			want: ServiceDataTypePython,
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
