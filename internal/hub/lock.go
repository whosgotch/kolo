package hub

import (
	"fmt"
	"os"
	"syscall"
)

// lock takes an exclusive lock covering the org file at path, held until the
// returned function is called.
//
// The lock lives on a file of its own because the org file is replaced by
// rename: a lock on the inode one writer opened says nothing to the writer
// that has already swapped a new inode into the name.
func lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("hub: lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("hub: lock %s: %w", path, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
