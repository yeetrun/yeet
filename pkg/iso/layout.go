// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iso

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

const (
	PreferredPool    = "172.30.0.0/16"
	AllocatorVersion = 1
	PolicyVersion    = 1
	MaxLinks         = 8192
	MaxProjects      = 1024
	MaxComponents    = 29
)

var (
	ErrLinkCapacity      = errors.New("ISO link capacity exhausted")
	ErrProjectCapacity   = errors.New("ISO project capacity exhausted")
	ErrComponentCapacity = errors.New("ISO project supports at most 29 active components")
)

type Layout struct {
	Pool     netip.Prefix
	Links    netip.Prefix
	Projects netip.Prefix
}

func NewLayout(pool netip.Prefix) (Layout, error) {
	pool = pool.Masked()
	if !pool.IsValid() || !pool.Addr().Is4() || pool.Bits() != 16 {
		return Layout{}, fmt.Errorf("ISO pool must be an IPv4 /16: %v", pool)
	}
	projectBase, err := addIPv4(pool.Addr(), 1<<15)
	if err != nil {
		return Layout{}, err
	}
	return Layout{
		Pool:     pool,
		Links:    netip.PrefixFrom(pool.Addr(), 17),
		Projects: netip.PrefixFrom(projectBase, 17),
	}, nil
}

func (l Layout) Link(index int) (netip.Prefix, error) {
	if index < 0 || index >= MaxLinks {
		return netip.Prefix{}, ErrLinkCapacity
	}
	addr, err := addIPv4(l.Links.Addr(), uint32(index*4))
	return netip.PrefixFrom(addr, 30), err
}

func (l Layout) Project(index int) (netip.Prefix, error) {
	if index < 0 || index >= MaxProjects {
		return netip.Prefix{}, ErrProjectCapacity
	}
	addr, err := addIPv4(l.Projects.Addr(), uint32(index*32))
	return netip.PrefixFrom(addr, 27), err
}

func addIPv4(addr netip.Addr, offset uint32) (netip.Addr, error) {
	if !addr.Is4() {
		return netip.Addr{}, fmt.Errorf("address is not IPv4: %v", addr)
	}
	raw := addr.As4()
	base := binary.BigEndian.Uint32(raw[:])
	if ^uint32(0)-base < offset {
		return netip.Addr{}, fmt.Errorf("IPv4 address overflow from %v", addr)
	}
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], base+offset)
	return netip.AddrFrom4(out), nil
}
