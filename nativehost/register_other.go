//go:build !windows

package nativehost

func IsBrowserInvocation([]string) bool { return false }
func Register() error                   { return nil }
