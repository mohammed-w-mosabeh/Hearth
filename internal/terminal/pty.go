package terminal

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// ioctls of include/uapi/asm-generic/ioctls.h.
const (
	tiocsptlck  = 0x40045431 // unlock the pty pair
	tiocgptpeer = 0x5441     // open the peer (slave) end, Linux 4.13+
	tiocswinsz  = 0x5414
)

// openPTY opens a pseudo-terminal pair. The slave end is opened from the
// master (TIOCGPTPEER), so it never goes through a /dev/pts path: hearthd
// has a private /dev (PrivateDevices=yes) and the shell receives the fd.
func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("terminal: ptmx: %w", err)
	}
	unlock := int32(0)
	if err := ioctl(m.Fd(), tiocsptlck, uintptr(unsafe.Pointer(&unlock))); err != nil { // #nosec G103 -- ioctl argument
		_ = m.Close()
		return nil, nil, fmt.Errorf("terminal: unlockpt: %w", err)
	}
	fd, _, errno := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), tiocgptpeer, uintptr(syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC))
	if errno != 0 {
		_ = m.Close()
		return nil, nil, fmt.Errorf("terminal: open peer: %w", errno)
	}
	return m, os.NewFile(fd, "pty-slave"), nil
}

// winsize is struct winsize.
type winsize struct {
	rows, cols, x, y uint16
}

// setSize sets the terminal size (the kernel signals SIGWINCH to the
// foreground process group).
func setSize(f *os.File, cols, rows int) error {
	ws := winsize{rows: uint16(rows), cols: uint16(cols)}          // #nosec G115 -- 1..1000, checked by the caller
	return ioctl(f.Fd(), tiocswinsz, uintptr(unsafe.Pointer(&ws))) // #nosec G103 -- ioctl argument
}

func ioctl(fd, req, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); errno != 0 {
		return errno
	}
	return nil
}

// dupFd duplicates fd with close-on-exec (tests stand in for systemd).
func dupFd(fd int) (int, error) {
	d, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_DUPFD_CLOEXEC, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(d), nil
}
