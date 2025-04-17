// Copyright 2018 Google LLC All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authn

import (
	"reflect"
	"testing"
)

func TestBasic(t *testing.T) {
	basic := &Basic{Username: "foo", Password: "bar"}

	got, err := basic.Authorization()
	if err != nil {
		t.Fatalf("Authorization() = %v", err)
	}
	want := &AuthConfig{Username: "foo", Password: "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Authorization(); got %v, want %v", got, want)
	}
}
