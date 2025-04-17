// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cronutil

import (
	"fmt"
	"strings"
)

// CronToCalender converts a cron expression to a systemd timer calendar event.
func CronToCalender(cron string) (string, error) {
	var cal string

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

	minute := parts[0]
	hour := parts[1]
	dayOfMonth := parts[2]
	month := parts[3]
	dayOfWeek := parts[4]

	// Convert cron minute to systemd minute (add leading zero if needed)
	if minute != "*" {
		minute = fmt.Sprintf("%02s", minute)
	}

	// Convert cron hour to systemd hour (add leading zero if needed)
	if hour != "*" {
		hour = fmt.Sprintf("%02s", hour)
	}

	if month != "*" {
		ms := strings.Split(month, ",")
		// pad single digit months with a leading zero
		for i, m := range ms {
			if len(m) == 1 {
				ms[i] = "0" + m
			}
		}
		month = strings.Join(ms, ",")
	}

	// Handle intervals like "*/7" for dayOfMonth
	if strings.HasPrefix(dayOfMonth, "*/") {
		minute = fmt.Sprintf("%s%s", minute, strings.TrimPrefix(dayOfMonth, "*"))
		dayOfMonth = "*"
	} else if dayOfMonth != "*" {
		dayOfMonth = fmt.Sprintf("%02s", dayOfMonth)
	}

	// Convert month to systemd format (add leading zero if needed)
	if month != "*" && len(month) == 1 {
		month = "0" + month
	}

	// Map dayOfWeek from cron numbers to systemd names (0 = Sunday, 1 = Monday, etc.)
	dayOfWeekMap := map[string]string{
		"0": "Sun", "1": "Mon", "2": "Tue", "3": "Wed", "4": "Thu", "5": "Fri", "6": "Sat",
	}

	// Handle ranges and multiple days for dayOfWeek
	if dayOfWeek == "*" {
		dayOfWeek = ""
	} else {
		dayOfWeek = convertDayOfWeek(dayOfWeek, dayOfWeekMap)
	}

	// Construct systemd calendar format without seconds
	cal = fmt.Sprintf("%s *-%s-%s %s:%s", dayOfWeek, month, dayOfMonth, hour, minute)
	cal = strings.TrimSpace(cal) // Trim leading whitespace if dayOfWeek is empty

	return cal, nil
}

// convertDayOfWeek handles day of the week conversion, including ranges and lists.
func convertDayOfWeek(dayOfWeek string, dayOfWeekMap map[string]string) string {
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
