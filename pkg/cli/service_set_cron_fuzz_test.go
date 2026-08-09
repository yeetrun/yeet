// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cli

import (
	"reflect"
	"testing"
)

func FuzzParseServiceSetCron(f *testing.F) {
	for _, seed := range []string{"0 3 * * *", " 30  2 * * * ", "", "* * * * *"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		flags, args, err := ParseServiceSet([]string{"reports", "--cron=" + raw})
		if err != nil {
			return
		}
		if !flags.CronSet || flags.Cron == "" || !reflect.DeepEqual(args, []string{"reports"}) {
			t.Fatalf("successful parse lost cron state: %#v %#v", flags, args)
		}
		reparsed, reparsedArgs, err := ParseServiceSet([]string{"reports", "--cron=" + flags.Cron})
		if err != nil || reparsed.Cron != flags.Cron || !reparsed.CronSet || !reflect.DeepEqual(reparsedArgs, args) {
			t.Fatalf("canonical parse is unstable: %#v %#v %v", reparsed, reparsedArgs, err)
		}
	})
}
