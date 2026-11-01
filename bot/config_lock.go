package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// acquireConfigLock uses the same lock file and POSIX advisory lock as freedom.sh.
// A finite timeout prevents Telegram polling from hanging indefinitely.
func acquireConfigLock(configPath string, timeout time.Duration) (*os.File, error) {
	lockPath := filepath.Join(filepath.Dir(configPath), ".freedom-config.lock")
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			file.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			file.Close()
			return nil, fmt.Errorf("Configuration lock busy after waiting %s", timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func releaseConfigLock(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}
