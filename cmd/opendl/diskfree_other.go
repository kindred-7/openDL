//go:build !windows

package main

// diskSpace is unavailable outside Windows.
func diskSpace(path string) (free, total int64) { return -1, -1 }

// diskStatus is unavailable outside Windows; diskStats falls back to dir.
func diskStatus() []diskStat { return nil }
