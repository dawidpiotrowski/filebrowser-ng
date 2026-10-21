package fileutils

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

func isCrossDevice(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) || errors.Is(err, syscall.EXDEV)
}
