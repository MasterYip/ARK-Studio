//go:build linux

package workspace

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// PTYAvailability is pty_availability() on Linux: the posix backend.
func PTYAvailability() PTYInfo {
	return PTYInfo{Available: true, Backend: "posix"}
}

type ptyProcess interface {
	handle
	read(buf []byte) (int, error)
	command() *exec.Cmd
}

type posixPTY struct {
	cmd    *exec.Cmd
	master *os.File
}

// startPTY is _PosixPtyHandle: /dev/ptmx plus a session-leader child that
// adopts the slave as its controlling terminal.
func startPTY(text, dir string, env []string, cols, rows int) (ptyProcess, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		unix.Close(fd)
		return nil, err
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	defer slave.Close()
	ws := &unix.Winsize{Row: uint16(rows), Col: uint16(cols)}
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, ws); err != nil {
		unix.Close(fd)
		return nil, err
	}
	argv := shellArgv(text)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		unix.Close(fd)
		return nil, err
	}
	unix.SetNonblock(fd, true) //nolint:errcheck // lets Close interrupt a blocked read
	return &posixPTY{cmd: cmd, master: os.NewFile(uintptr(fd), "/dev/ptmx")}, nil
}

func (p *posixPTY) pid() int           { return p.cmd.Process.Pid }
func (p *posixPTY) command() *exec.Cmd { return p.cmd }
func (p *posixPTY) close()             { p.master.Close() }

func (p *posixPTY) read(buf []byte) (int, error) {
	n, err := p.master.Read(buf)
	if errors.Is(err, syscall.EIO) {
		err = os.ErrClosed // the slave side is gone: EOF
	}
	return n, err
}

func (p *posixPTY) write(data []byte) error {
	_, err := p.master.Write(data)
	return err
}

func (p *posixPTY) resize(cols, rows int) error {
	conn, err := p.master.SyscallConn()
	if err != nil {
		return err
	}
	var ioErr error
	err = conn.Control(func(fd uintptr) {
		ioErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
	})
	if err != nil {
		return err
	}
	return ioErr
}
