//go:build !windows

package download

import "os"

func platformOutputLimit(*os.File) int64 { return 0 }
