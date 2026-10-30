package main

import "time"

// nightDaysAllow 报告当前时间是否在重复日期内；空表表示每天。日期使用
// time.Weekday 的取值（0 为周日）。
func nightDaysAllow(days []int, now time.Time) bool {
	if len(days) == 0 {
		return true
	}
	today := int(now.Weekday())
	for _, day := range days {
		if day == today {
			return true
		}
	}
	return false
}

// normalizeNightDays 去重并丢弃越界日期；七天齐全等价于每天，存空表。
func normalizeNightDays(days []int) []int {
	out := make([]int, 0, len(days))
	for _, day := range days {
		if day < 0 || day > 6 {
			continue
		}
		duplicate := false
		for _, existing := range out {
			if existing == day {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, day)
		}
	}
	if len(out) == 7 {
		return nil
	}
	return out
}

// nightDue finds the most recent scheduled time crossed since the previous
// observation. A new session only considers the current minute, not times
// missed while the application was closed.
func nightDue(last, now time.Time, at string) (time.Time, bool) {
	clock, err := time.Parse("15:04", at)
	if err != nil {
		return time.Time{}, false
	}
	if last.IsZero() {
		last = now.Truncate(time.Minute).Add(-time.Nanosecond)
	}
	if now.Before(last) {
		return time.Time{}, false
	}
	due := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location())
	if due.After(now) {
		previous := now.AddDate(0, 0, -1)
		due = time.Date(previous.Year(), previous.Month(), previous.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location())
	}
	// A nonexistent local time during a DST jump should not run early.
	if due.Format("15:04") != at {
		return time.Time{}, false
	}
	// Keep checking during the scheduled minute so tasks added in that
	// minute retain the existing behavior.
	return due, !due.After(now) && (due.After(last) || now.Sub(due) < time.Minute)
}
