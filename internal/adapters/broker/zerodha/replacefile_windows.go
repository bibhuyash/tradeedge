//go:build windows

package zerodha

import (
	"os"
	"syscall"
	"unsafe"
)

const moveFileReplaceExisting = 0x1

func replaceFile(source, destination string) error {
	sourcePointer, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	moveFileEx := kernel32.NewProc("MoveFileExW")
	result, _, callErr := moveFileEx.Call(uintptr(unsafe.Pointer(sourcePointer)), uintptr(unsafe.Pointer(destinationPointer)), moveFileReplaceExisting)
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return os.ErrInvalid
	}
	return nil
}
