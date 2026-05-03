// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cronutil

import (
	"fmt"
	"strings"
)

var dayOfWeekMap = map[string]string{
	"0": "Sun",
	"1": "Mon",
	"2": "Tue",
	"3": "Wed",
	"4": "Thu",
	"5": "Fri",
	"6": "Sat",
}

type cronSchedule struct {
	minute     string
	hour       string
	dayOfMonth string
	month      string
	dayOfWeek  string
}

// CronToCalender converts a cron expression to a systemd timer calendar event.
func CronToCalender(cron string) (string, error) {
	// `cron` is of the form `* * * * *`: "m h dom mon dow".
	// `cal` is of the form  `* *-*-* *:*`: "dow y-m-d h:M".

	// Parse the cron expression.
	parts := strings.Fields(cron)
	if len(parts) != 5 {
		return "", fmt.Errorf("invalid cron expression: %q", cron)
	}

	// Fast path every minute.
	if strings.Join(parts, " ") == "* * * * *" {
		return "*-*-* *:*:00", nil
	}

	schedule := cronSchedule{
		minute:     parts[0],
		hour:       parts[1],
		dayOfMonth: parts[2],
		month:      parts[3],
		dayOfWeek:  parts[4],
	}
	schedule.normalize()
	return schedule.calendar(), nil
}

func (s *cronSchedule) normalize() {
	s.minute = padCronField(s.minute)
	s.hour = padCronField(s.hour)
	s.month = normalizeMonth(s.month)
	s.dayOfMonth, s.minute = normalizeDayOfMonth(s.dayOfMonth, s.minute)
	s.dayOfWeek = normalizeDayOfWeek(s.dayOfWeek)
}

func (s cronSchedule) calendar() string {
	cal := fmt.Sprintf("%s *-%s-%s %s:%s", s.dayOfWeek, s.month, s.dayOfMonth, s.hour, s.minute)
	return strings.TrimSpace(cal)
}

func padCronField(value string) string {
	if value == "*" {
		return value
	}
	return fmt.Sprintf("%02s", value)
}

func normalizeMonth(month string) string {
	if month == "*" {
		return month
	}
	ms := strings.Split(month, ",")
	for i, m := range ms {
		ms[i] = padSingleDigit(m)
	}
	return strings.Join(ms, ",")
}

func normalizeDayOfMonth(dayOfMonth, minute string) (string, string) {
	if strings.HasPrefix(dayOfMonth, "*/") {
		minute = fmt.Sprintf("%s%s", minute, strings.TrimPrefix(dayOfMonth, "*"))
		return "*", minute
	}
	return padCronField(dayOfMonth), minute
}

func normalizeDayOfWeek(dayOfWeek string) string {
	if dayOfWeek == "*" {
		return ""
	}
	return convertDayOfWeek(dayOfWeek)
}

// convertDayOfWeek handles day of the week conversion, including ranges and lists.
func convertDayOfWeek(dayOfWeek string) string {
	// Handle day ranges like "1-5" (Mon-Fri)
	if strings.Contains(dayOfWeek, "-") {
		parts := strings.Split(dayOfWeek, "-")
		if len(parts) == 2 {
			start, end := dayOfWeekMap[parts[0]], dayOfWeekMap[parts[1]]
			return fmt.Sprintf("%s...%s", start, end)
		}
	}

	// Handle multiple days like "6,0" (Sat, Sun)
	if strings.Contains(dayOfWeek, ",") {
		parts := strings.Split(dayOfWeek, ",")
		var mappedParts []string
		for _, part := range parts {
			mappedParts = append(mappedParts, dayOfWeekMap[part])
		}
		return strings.Join(mappedParts, ",")
	}

	// Handle single day mapping
	if dow, ok := dayOfWeekMap[dayOfWeek]; ok {
		return dow
	}

	// Return as-is if no mapping is found
	return dayOfWeek
}

func padSingleDigit(value string) string {
	if len(value) == 1 {
		return "0" + value
	}
	return value
}
