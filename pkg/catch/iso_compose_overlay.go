// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"path/filepath"

	"github.com/yeetrun/yeet/pkg/db"
	"gopkg.in/yaml.v3"
)

type isoComposeOverlay struct {
	Services map[string]isoComposeOverlayService `yaml:"services"`
	Networks map[string]isoComposeOverlayNetwork `yaml:"networks"`
}

type isoComposeOverlayService struct {
	Networks map[string]isoComposeServiceNetwork `yaml:"networks"`
	DNS      []string                            `yaml:"dns"`
}

type isoComposeServiceNetwork struct {
	IPv4Address string `yaml:"ipv4_address"`
}

type isoComposeOverlayNetwork struct {
	Driver     string            `yaml:"driver"`
	DriverOpts map[string]string `yaml:"driver_opts"`
	EnableIPv6 bool              `yaml:"enable_ipv6"`
	IPAM       isoComposeIPAM    `yaml:"ipam"`
}

type isoComposeIPAM struct {
	Config []isoComposeIPAMConfig `yaml:"config"`
}

type isoComposeIPAMConfig struct {
	Subnet  string `yaml:"subnet"`
	Gateway string `yaml:"gateway"`
}

func renderISOComposeOverlay(allocation *db.ISOAllocation, model ISOComposeModel) (string, error) {
	if allocation == nil {
		return "", fmt.Errorf("ISO container allocation is incomplete")
	}
	if err := validateISOPersistedOverlay("networks.default", allocation); err != nil {
		return "", fmt.Errorf("ISO container allocation is incomplete: %w", err)
	}

	resolver := isoComposeResolver(allocation)
	overlay := isoComposeOverlay{
		Services: map[string]isoComposeOverlayService{},
		Networks: map[string]isoComposeOverlayNetwork{
			"default": {
				Driver: "yeet",
				DriverOpts: map[string]string{
					"dev.catchit.netns": filepath.Join(isoDockerNetNSRoot, allocation.NetNS),
					"dev.catchit.mode":  "iso",
				},
				EnableIPv6: false,
				IPAM: isoComposeIPAM{Config: []isoComposeIPAMConfig{{
					Subnet:  allocation.Project.Masked().String(),
					Gateway: allocation.Gateway.String(),
				}}},
			},
		},
	}
	for _, name := range model.Components {
		component, ok := allocation.Components[name]
		if !ok || !component.Address.IsValid() {
			return "", fmt.Errorf("ISO component %q has no reserved address", name)
		}
		overlay.Services[name] = isoComposeOverlayService{
			Networks: map[string]isoComposeServiceNetwork{
				"default": {IPv4Address: component.Address.String()},
			},
			DNS: []string{resolver},
		}
	}
	raw, err := yaml.Marshal(overlay)
	if err != nil {
		return "", fmt.Errorf("marshal ISO Compose overlay: %w", err)
	}
	return string(raw), nil
}
