package files

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func etagIdentity(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return fmt.Sprintf("%x-%x", stat.CreationTime.HighDateTime, stat.CreationTime.LowDateTime)
	}
	return ""
}

// Reopen the private newly-created handle with owner/DACL access. Failure to
// preserve security metadata aborts publication, leaving the old file intact.
func preserveMetadata(src, dst *os.File, _ os.FileInfo) error {
	sd, err := windows.GetSecurityInfo(windows.Handle(src.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	group, _, err := sd.Group()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	var handle windows.Handle
	err = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(dst.Fd()), windows.CurrentProcess(), &handle, windows.WRITE_DAC|windows.WRITE_OWNER, false, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	flags := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION)
	if control&windows.SE_DACL_PROTECTED != 0 {
		flags |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		flags |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, flags, owner, group, dacl, nil)
}
