//go:build darwin || linux

package logger

import (
	"io/fs"
	"os"
	"syscall"
)

func openActiveFile(path string, flags int, mode fs.FileMode) (activeFile, error) {
	return os.OpenFile(path, flags|syscall.O_NOFOLLOW, mode)
}
