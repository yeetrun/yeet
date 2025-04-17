// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yargs

import (
	"reflect"
	"testing"
)

func TestConsumeFlagsBySpec(t *testing.T) {
	specs := map[string]ConsumeSpec{
		"host": {Kind: reflect.String},
	}
	args := []string{"--host", "catch", "status"}
	remaining, values := ConsumeFlagsBySpec(args, specs)

	if got := values["host"]; !reflect.DeepEqual(got, []string{"catch"}) {
		t.Fatalf("values[host] = %#v, want %#v", got, []string{"catch"})
	}
	if !reflect.DeepEqual(remaining, []string{"status"}) {
		t.Fatalf("remaining = %#v, want %#v", remaining, []string{"status"})
	}
}

func TestConsumeFlagsBySpecEquals(t *testing.T) {
	specs := map[string]ConsumeSpec{
		"host": {Kind: reflect.String},
	}
	args := []string{"status", "--host=catch"}
	remaining, values := ConsumeFlagsBySpec(args, specs)

	if got := values["host"]; !reflect.DeepEqual(got, []string{"catch"}) {
		t.Fatalf("values[host] = %#v, want %#v", got, []string{"catch"})
	}
	if !reflect.DeepEqual(remaining, []string{"status"}) {
		t.Fatalf("remaining = %#v, want %#v", remaining, []string{"status"})
	}
}

func TestConsumeFlagsBySpecUnknownPreserved(t *testing.T) {
	specs := map[string]ConsumeSpec{
		"host": {Kind: reflect.String},
	}
	args := []string{"--unknown", "x", "--host", "catch"}
	remaining, values := ConsumeFlagsBySpec(args, specs)

	if got := values["host"]; !reflect.DeepEqual(got, []string{"catch"}) {
		t.Fatalf("values[host] = %#v, want %#v", got, []string{"catch"})
	}
	if !reflect.DeepEqual(remaining, []string{"--unknown", "x"}) {
		t.Fatalf("remaining = %#v, want %#v", remaining, []string{"--unknown", "x"})
	}
}

func TestConsumeFlagsBySpecDoubleDash(t *testing.T) {
	specs := map[string]ConsumeSpec{
		"host": {Kind: reflect.String},
	}
	args := []string{"--host", "catch", "--", "--host", "ignored"}
	remaining, values := ConsumeFlagsBySpec(args, specs)

	if got := values["host"]; !reflect.DeepEqual(got, []string{"catch"}) {
		t.Fatalf("values[host] = %#v, want %#v", got, []string{"catch"})
	}
	if !reflect.DeepEqual(remaining, []string{"--", "--host", "ignored"}) {
		t.Fatalf("remaining = %#v, want %#v", remaining, []string{"--", "--host", "ignored"})
	}
}

func TestConsumeFlagsBySpecSplitComma(t *testing.T) {
	specs := map[string]ConsumeSpec{
		"tags": {Kind: reflect.Slice, SplitComma: true},
	}
	args := []string{"--tags", "tag:a,tag:b", "--tags", "tag:c"}
	remaining, values := ConsumeFlagsBySpec(args, specs)

	if got := values["tags"]; !reflect.DeepEqual(got, []string{"tag:a", "tag:b", "tag:c"}) {
		t.Fatalf("values[tags] = %#v, want %#v", got, []string{"tag:a", "tag:b", "tag:c"})
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining = %#v, want empty", remaining)
	}
}

func TestConsumeFlagsBySpecBoolDoesNotConsumeNext(t *testing.T) {
	specs := map[string]ConsumeSpec{
		"verbose": {Kind: reflect.Bool},
	}
	args := []string{"--verbose", "value", "rest"}
	remaining, values := ConsumeFlagsBySpec(args, specs)

	if got := values["verbose"]; !reflect.DeepEqual(got, []string{"true"}) {
		t.Fatalf("values[verbose] = %#v, want %#v", got, []string{"true"})
	}
	if !reflect.DeepEqual(remaining, []string{"value", "rest"}) {
		t.Fatalf("remaining = %#v, want %#v", remaining, []string{"value", "rest"})
	}
}

func TestParseKnownFlags(t *testing.T) {
	type Flags struct {
		Host string `flag:"host"`
	}

	result, err := ParseKnownFlags[Flags]([]string{"--host", "catch", "status"}, KnownFlagsOptions{})
	if err != nil {
		t.Fatalf("ParseKnownFlags error: %v", err)
	}
	if result.Flags.Host != "catch" {
		t.Fatalf("Host = %q, want %q", result.Flags.Host, "catch")
	}
	if !reflect.DeepEqual(result.RemainingArgs, []string{"status"}) {
		t.Fatalf("Remaining = %#v, want %#v", result.RemainingArgs, []string{"status"})
	}
}

func TestParseKnownFlagsSplitComma(t *testing.T) {
	type Flags struct {
		Tags []string `flag:"tags"`
	}

	result, err := ParseKnownFlags[Flags]([]string{"--tags", "tag:a,tag:b", "--tags", "tag:c"}, KnownFlagsOptions{SplitCommaSlices: true})
	if err != nil {
		t.Fatalf("ParseKnownFlags error: %v", err)
	}
	if !reflect.DeepEqual(result.Flags.Tags, []string{"tag:a", "tag:b", "tag:c"}) {
		t.Fatalf("Tags = %#v, want %#v", result.Flags.Tags, []string{"tag:a", "tag:b", "tag:c"})
	}
	if len(result.RemainingArgs) != 0 {
		t.Fatalf("Remaining = %#v, want empty", result.RemainingArgs)
	}
}

func TestParseKnownFlagsShortAlias(t *testing.T) {
	type Flags struct {
		Tags []string `flag:"tags" short:"t"`
	}

	result, err := ParseKnownFlags[Flags]([]string{"-t", "tag:a", "rest"}, KnownFlagsOptions{})
	if err != nil {
		t.Fatalf("ParseKnownFlags error: %v", err)
	}
	if !reflect.DeepEqual(result.Flags.Tags, []string{"tag:a"}) {
		t.Fatalf("Tags = %#v, want %#v", result.Flags.Tags, []string{"tag:a"})
	}
	if !reflect.DeepEqual(result.RemainingArgs, []string{"rest"}) {
		t.Fatalf("Remaining = %#v, want %#v", result.RemainingArgs, []string{"rest"})
	}
}
