package main

import (
	"os"
	"syscall"
	"time"
)

// createdAt is when the file was created.
func createdAt(info os.FileInfo) time.Time {
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, d.CreationTime.Nanoseconds())
	}
	return info.ModTime()
}
