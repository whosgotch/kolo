package host

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LockState gives one host process ownership of a state file until unlock is
// called. Two hosts writing the same file would each erase agents known only
// to the other and make restart recovery depend on which one wrote last.
func LockState(path string) (unlock func(), err error) {
	if path == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("host: make state directory: %w", err)
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("host: lock state %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("host: state %s is already in use by another kolo host", path)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
