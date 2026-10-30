package main

import (
	"testing"
	"time"
)

func TestNightDueAcrossSleepAndNewSession(t *testing.T) {
	loc := time.FixedZone("local", 8*60*60)
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 10, day, hour, minute, 0, 0, loc) }
	for _, tc := range []struct {
		name      string
		last, now time.Time
		want      bool
	}{
		{"crossed during sleep", at(27, 0, 50), at(27, 1, 20), true},
		{"crossed midnight", at(27, 23, 50), at(28, 1, 20), true},
		{"new session after schedule", time.Time{}, at(27, 1, 20), false},
		{"new session during minute", time.Time{}, at(27, 1, 0), true},
		{"still within minute", at(27, 1, 0), at(27, 1, 0).Add(30 * time.Second), true},
		{"before schedule", at(27, 0, 30), at(27, 0, 50), false},
		{"clock moved backwards", at(27, 2, 0), at(27, 1, 30), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got := nightDue(tc.last, tc.now, "01:00")
			if got != tc.want {
				t.Fatalf("due=%v, want %v", got, tc.want)
			}
		})
	}
}
