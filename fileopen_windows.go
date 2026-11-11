//go:build windows

package logger

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func openActiveFile(path string, flags int, mode fs.FileMode) (activeFile, error) {
	if flags&os.O_CREATE == 0 || flags&os.O_WRONLY == 0 {
		return nil, fs.ErrInvalid
	}

	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	disposition := uint32(windows.OPEN_ALWAYS)
	if flags&os.O_EXCL != 0 {
		disposition = windows.CREATE_NEW
	}

	handle, err := windows.CreateFile(
		name,
		windows.FILE_APPEND_DATA|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}

	f := os.NewFile(uintptr(handle), path)
	if f == nil {
		_ = windows.CloseHandle(handle)
		return nil, fs.ErrInvalid
	}

	var fileInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &fileInfo); err != nil {
		_ = f.Close()
		return nil, err
	}

	if fileInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = f.Close()
		return nil, fs.ErrInvalid
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fs.ErrInvalid
	}

	return f, nil
}
