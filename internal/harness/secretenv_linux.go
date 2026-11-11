//go:build linux

package harness

import (
	"errors"
	"os"
	"strconv"
	"syscall"
)

const prSetDumpable = 4

func hardenProcess() error {
	if err := markRemainingCloexec(); err != nil {
		return err
	}
	return setNonDumpable()
}

func markRemainingCloexec() error {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return errors.New("harness: descriptors")
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd <= 2 {
			continue
		}
		_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFD), uintptr(syscall.FD_CLOEXEC))
		if errno != 0 && errno != syscall.EBADF {
			return errors.New("harness: descriptors")
		}
	}
	return nil
}

func setNonDumpable() error {
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetDumpable, 0, 0, 0, 0, 0)
	if errno != 0 {
		return errors.New("harness: dumpable")
	}
	return nil
}
