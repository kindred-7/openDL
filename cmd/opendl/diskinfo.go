package main

import "path/filepath"

// diskStat 是一个卷的容量状态，用于左下卡片的磁盘列表。
type diskStat struct {
	name        string
	free, total int64
}

// diskStats 列出所有磁盘；无法枚举卷的平台回退到 dir 所在磁盘。
func diskStats(dir string) []diskStat {
	if list := diskStatus(); len(list) > 0 {
		return list
	}
	free, total := diskSpace(dir)
	name := filepath.VolumeName(dir)
	if name == "" {
		name = "磁盘"
	}
	return []diskStat{{name: name, free: free, total: total}}
}
