// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cronutil

import "testing"

func TestCronToCalendar(t *testing.T) {
	tests := []struct {
		name      string
		cronExpr  string
		wantCal   string
		wantError bool
	}{
		{"Every Minute", "* * * * *", "*-*-* *:*:00", false},
		{"Every 2 Minutes", "*/2 * * * *", "*-*-* *:*/2", false},
		{"Every 5 Minutes", "*/5 * * * *", "*-*-* *:*/5", false},
		{"Every 15 Minutes", "*/15 * * * *", "*-*-* *:*/15", false},
		{"Every Quarter Hour", "*/15 * * * *", "*-*-* *:*/15", false},
		{"Every 30 Minutes", "*/30 * * * *", "*-*-* *:*/30", false},
		{"Every Half Hour", "*/30 * * * *", "*-*-* *:*/30", false},
		{"Every 1 Hour", "0 * * * *", "*-*-* *:00", false},
		{"Every 2 Hours", "0 */2 * * *", "*-*-* */2:00", false},
		{"Every 3 Hours", "0 */3 * * *", "*-*-* */3:00", false},
		{"Every Other Hour", "0 */2 * * *", "*-*-* */2:00", false},
		{"Every 6 Hours", "0 */6 * * *", "*-*-* */6:00", false},
		{"Every 12 Hours", "0 */12 * * *", "*-*-* */12:00", false},
		{"Hour Range", "0 9-17 * * *", "*-*-* 9-17:00", false},
		{"Between Certain Hours", "0 9-17 * * *", "*-*-* 9-17:00", false},
		{"Every Day", "0 0 * * *", "*-*-* 00:00", false},
		{"Daily", "0 0 * * *", "*-*-* 00:00", false},
		{"Once A Day", "0 0 * * *", "*-*-* 00:00", false},
		{"Every Night", "0 1 * * *", "*-*-* 01:00", false},
		{"Every Day at 1am", "0 1 * * *", "*-*-* 01:00", false},
		{"Every Day at 2am", "0 2 * * *", "*-*-* 02:00", false},
		{"Every Morning", "0 7 * * *", "*-*-* 07:00", false},
		{"Every Midnight", "0 0 * * *", "*-*-* 00:00", false},
		{"Every Day at Midnight", "0 0 * * *", "*-*-* 00:00", false},
		{"Every Night at Midnight", "0 0 * * *", "*-*-* 00:00", false},
		{"Every Sunday", "0 0 * * 0", "Sun *-*-* 00:00", false},
		{"Every Friday", "0 1 * * 5", "Fri *-*-* 01:00", false},
		{"Every Friday at Midnight", "0 0 * * 5", "Fri *-*-* 00:00", false},
		{"Every Saturday", "0 0 * * 6", "Sat *-*-* 00:00", false},
		{"Every Weekday", "0 0 * * 1-5", "Mon...Fri *-*-* 00:00", false},
		{"Weekdays Only", "0 0 * * 1-5", "Mon...Fri *-*-* 00:00", false},
		{"Monday to Friday", "0 0 * * 1-5", "Mon...Fri *-*-* 00:00", false},
		{"Every Weekend", "0 0 * * 6,0", "Sat,Sun *-*-* 00:00", false},
		{"Weekends Only", "0 0 * * 6,0", "Sat,Sun *-*-* 00:00", false},
		{"Every 7 Days", "0 0 */7 * *", "*-*-* 00:00/7", false},
		{"Every Week", "0 0 * * 0", "Sun *-*-* 00:00", false},
		{"Weekly", "0 0 * * 0", "Sun *-*-* 00:00", false},
		{"Once a Week", "0 0 * * 0", "Sun *-*-* 00:00", false},
		{"Every Month", "0 0 1 * *", "*-*-01 00:00", false},
		{"Monthly", "0 0 1 * *", "*-*-01 00:00", false},
		{"Once a Month", "0 0 1 * *", "*-*-01 00:00", false},
		{"Every Quarter", "0 0 1 1,4,7,10 *", "*-01,04,07,10-01 00:00", false},
		{"Every 6 Months", "0 0 1 1,7 *", "*-01,07-01 00:00", false},
		{"Every Year", "0 0 1 1 *", "*-01-01 00:00", false},
		{"Every 15th at 9:00 AM", "0 9 15 * *", "*-*-15 09:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCal, err := CronToCalender(tt.cronExpr)
			if (err != nil) != tt.wantError {
				t.Errorf("CronToCalender() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if gotCal != tt.wantCal {
				t.Errorf("CronToCalender() got = %v, want %v", gotCal, tt.wantCal)
			}
		})
	}
}
