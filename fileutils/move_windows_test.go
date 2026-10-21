package fileutils

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsCrossDeviceError(t *testing.T) {
	if !isCrossDevice(&os.LinkError{Op: "rename", Err: windows.ERROR_NOT_SAME_DEVICE}) {
		t.Fatal("Windows cross-volume rename error was not recognized")
	}
	if isCrossDevice(windows.ERROR_ACCESS_DENIED) {
		t.Fatal("Windows permission failure must not trigger copy/delete")
	}
}
