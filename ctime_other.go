//go:build !windows

package main

import (
	"os"
	"time"
)

// createdAt is when the file was created, as far as the system tells:
// its last change elsewhere than on Windows.
func createdAt(info os.FileInfo) time.Time { return info.ModTime() }
