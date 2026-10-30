//go:build !windows

package main

import "image"

const fileIconsAvailable = false

func initFileIconThread() (func(), bool) { return nil, false }

func extractFileIcon(path string, actual bool) image.Image { return nil }
