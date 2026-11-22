package terminal

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// ioctls of include/uapi/asm-generic/ioctls.h.
const (
	tiocsptlck  = 0x40045431 // unlock the pty pair
	tiocgptpeer = 0x5441     // open the peer (slave) end, Linux 4.13+
	tiocswinsz  = 0x5414
)

// firstListenFD is SD_LISTEN_FDS_START.
const firstListenFD = 3

// ptmxPath is what openPTY opens. A bind mount of /dev/ptmx, which is what
// PrivateDevices=yes gives a user service, fails with ENOENT: the kernel
// cannot walk from it to the pts folder beside it. So the unit passes a
// descriptor of the host's node (OpenFile=/dev/ptmx:ptmx), and opening it
// again through /proc gives a new pty each time.
var ptmxPath = sync.OnceValue(func() string {
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) {
		for i, name := range strings.Split(os.Getenv("LISTEN_FDNAMES"), ":") {
			if name == "ptmx" {
				syscall.CloseOnExec(firstListenFD + i) // no child inherits it
				return "/proc/self/fd/" + strconv.Itoa(firstListenFD+i)
			}
		}
	}
	return "/dev/ptmx"
})

// openPTY opens a pseudo-terminal pair. The slave end is opened from the
// master (TIOCGPTPEER), so it never goes through a /dev/pts path: hearthd
// has a private /dev (PrivateDevices=yes) and the shell receives the fd.
func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile(ptmxPath(), os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("terminal: ptmx: %w", err)
	}
	unlock := int32(0)
	if err := ioctl(m, tiocsptlck, unsafe.Pointer(&unlock)); err != nil { // #nosec G103 -- ioctl argument
		_ = m.Close()
		return nil, nil, fmt.Errorf("terminal: unlockpt: %w", err)
	}
	var fd uintptr
	err = control(m, func(mfd uintptr) syscall.Errno {
		var errno syscall.Errno
		fd, _, errno = syscall.Syscall(syscall.SYS_IOCTL, mfd, tiocgptpeer, uintptr(syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC))
		return errno
	})
	if err != nil {
		_ = m.Close()
		return nil, nil, fmt.Errorf("terminal: open peer: %w", err)
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
	ws := winsize{rows: uint16(rows), cols: uint16(cols)} // #nosec G115 -- 1..1000, checked by the caller
	return ioctl(f, tiocswinsz, unsafe.Pointer(&ws))      // #nosec G103 -- ioctl argument
}

func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	return control(f, func(fd uintptr) syscall.Errno {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
		return errno
	})
}

// control runs fn on f's descriptor. Not through f.Fd(): that makes the file
// blocking, and then Close cannot end the read of an idle shell's output, so
// the shell and its slot outlive the browser tab.
func control(f *os.File, fn func(fd uintptr) syscall.Errno) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) { errno = fn(fd) }); err != nil {
		return err
	}
	if errno != 0 {
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
