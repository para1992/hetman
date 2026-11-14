package engine

import "syscall"

const errSharingViolation syscall.Errno = 32

// lockFile opens path with no sharing allowed, which excludes every other
// opener until the handle is closed or the process exits.
func lockFile(path string) (func(), error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if err == errSharingViolation {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() { syscall.CloseHandle(h) }, nil
}
