//go:build windows

package dvr

import (
	"syscall"
	"unsafe"
)

// diskSpace reports free and total bytes of the drive holding path.
func diskSpace(path string) (free, total uint64) {
	k := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0
	}
	var avail, tot, fr uint64
	r, _, _ := k.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&tot)), uintptr(unsafe.Pointer(&fr)))
	if r == 0 {
		return 0, 0
	}
	return avail, tot
}
