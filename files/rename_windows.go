package files

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Both names are single components, relative to already confined directory
// handles. Do not replace this with MoveFileEx on reconstructed absolute paths.
func renameAt(src *os.File, from string, dst *os.File, to string, replace bool) error {
	oldName, err := windows.NewNTUnicodeString(from)
	if err != nil {
		return err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(src.Fd()), ObjectName: oldName, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.DELETE|windows.SYNCHRONIZE, &oa, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return windowsError(err)
	}
	defer windows.CloseHandle(handle)
	name, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	type renameInfo struct {
		Replace byte
		Root    windows.Handle
		Length  uint32
		Name    [1]uint16
	}
	var layout renameInfo
	size := int(unsafe.Offsetof(layout.Name)) + (len(name)-1)*2
	buffer := make([]byte, size)
	info := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	if replace {
		info.Replace = 1
	}
	info.Root = windows.Handle(dst.Fd())
	info.Length = uint32((len(name) - 1) * 2)
	copy(unsafe.Slice(&info.Name[0], len(name)-1), name[:len(name)-1])
	return windowsError(windows.NtSetInformationFile(handle, &status, &buffer[0], uint32(size), windows.FileRenameInformation))
}

func windowsError(err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}

// Windows does not provide the same directory-fsync contract as Unix. File
// flush/close and atomic rename are enforced; power-loss durability is separate.
func syncDirectory(*os.File) error { return nil }
