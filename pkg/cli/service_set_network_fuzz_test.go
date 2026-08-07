// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cli

import (
	"reflect"
	"strconv"
	"testing"
)

func FuzzParseServiceSetNetwork(f *testing.F) {
	for _, seed := range []string{
		"svc --net=iso",
		"svc --ts-ver= --ts-exit= --ts-tags= --macvlan-parent= --macvlan-vlan= --macvlan-mac=",
		"svc --net=ts --ts-tags=tag:app --ts-tags=tag:ops",
		"svc --net=svc,",
		"svc --ts-auth-key=",
		"svc --net=lan --macvlan-vlan=42",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		flags, _, err := ParseServiceSet(fuzzArgs(raw))
		if err != nil {
			return
		}

		canonical := canonicalServiceSetNetworkArgs(flags)
		reparsed, _, err := ParseServiceSet(canonical)
		if err != nil {
			t.Fatal("canonical service network arguments did not parse")
		}
		if !equalServiceSetNetworkFlags(flags, reparsed) {
			t.Fatalf("canonical service network parse changed network flags: before=%#v after=%#v", networkFlagDebug(flags), networkFlagDebug(reparsed))
		}
	})
}

type safeNetworkFlagDebug struct {
	Net, TsVer, TsExit, MacvlanMac, MacvlanParent string
	TsTags                                        []string
	NetSet, TsVerSet, TsExitSet, TsTagsSet        bool
	TsAuthKeySet                                  bool
	MacvlanMacSet, MacvlanVlanSet                 bool
	MacvlanParentSet                              bool
	MacvlanVlan                                   int
}

func networkFlagDebug(flags ServiceSetFlags) safeNetworkFlagDebug {
	return safeNetworkFlagDebug{
		Net:              flags.Net,
		NetSet:           flags.NetSet,
		TsVer:            flags.TsVer,
		TsVerSet:         flags.TsVerSet,
		TsExit:           flags.TsExit,
		TsExitSet:        flags.TsExitSet,
		TsTags:           flags.TsTags,
		TsTagsSet:        flags.TsTagsSet,
		TsAuthKeySet:     flags.TsAuthKeySet,
		MacvlanMac:       flags.MacvlanMac,
		MacvlanMacSet:    flags.MacvlanMacSet,
		MacvlanVlan:      flags.MacvlanVlan,
		MacvlanVlanSet:   flags.MacvlanVlanSet,
		MacvlanParent:    flags.MacvlanParent,
		MacvlanParentSet: flags.MacvlanParentSet,
	}
}

func canonicalServiceSetNetworkArgs(flags ServiceSetFlags) []string {
	args := []string{"svc", "--run-as=app"}
	if flags.NetSet {
		args = append(args, "--net="+flags.Net)
	}
	if flags.TsVerSet {
		args = append(args, "--ts-ver="+flags.TsVer)
	}
	if flags.TsExitSet {
		args = append(args, "--ts-exit="+flags.TsExit)
	}
	if flags.TsTagsSet {
		if len(flags.TsTags) == 0 {
			args = append(args, "--ts-tags=")
		} else {
			for _, tag := range flags.TsTags {
				args = append(args, "--ts-tags="+tag)
			}
		}
	}
	if flags.TsAuthKeySet {
		args = append(args, "--ts-auth-key="+flags.TsAuthKey)
	}
	if flags.MacvlanMacSet {
		args = append(args, "--macvlan-mac="+flags.MacvlanMac)
	}
	if flags.MacvlanVlanSet {
		args = append(args, "--macvlan-vlan="+strconv.Itoa(flags.MacvlanVlan))
	}
	if flags.MacvlanParentSet {
		args = append(args, "--macvlan-parent="+flags.MacvlanParent)
	}
	return args
}

func equalServiceSetNetworkFlags(a, b ServiceSetFlags) bool {
	return a.Net == b.Net && a.NetSet == b.NetSet &&
		a.TsVer == b.TsVer && a.TsVerSet == b.TsVerSet &&
		a.TsExit == b.TsExit && a.TsExitSet == b.TsExitSet &&
		reflect.DeepEqual(a.TsTags, b.TsTags) && a.TsTagsSet == b.TsTagsSet &&
		a.TsAuthKey == b.TsAuthKey && a.TsAuthKeySet == b.TsAuthKeySet &&
		a.MacvlanMac == b.MacvlanMac && a.MacvlanMacSet == b.MacvlanMacSet &&
		a.MacvlanVlan == b.MacvlanVlan && a.MacvlanVlanSet == b.MacvlanVlanSet &&
		a.MacvlanParent == b.MacvlanParent && a.MacvlanParentSet == b.MacvlanParentSet
}
