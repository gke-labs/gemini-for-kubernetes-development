package sshd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"k8s.io/klog/v2"
)

// PTY represents a pseudo-terminal.
// It is a thin wrapper around the creak/pty library.
type PTY struct {
	pty *os.File
	tty *os.File
}

// sshWindowSize is the message sent in the "window-change" request
type sshWindowSize struct {
	Columns uint32
	Rows    uint32
	Width   uint32
	Height  uint32
}

// NewPTY creates a new PTY.
func NewPTY() (*PTY, error) {
	pty, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("opening pty: %w", err)
	}

	return &PTY{pty: pty, tty: tty}, nil
}

// SetSize sets the size of the PTY.
func (p *PTY) SetSize(size *sshWindowSize) error {
	if p.pty == nil {
		return fmt.Errorf("pty is closed")
	}
	ptySize := &pty.Winsize{
		Cols: uint16(size.Columns),
		Rows: uint16(size.Rows),
		X:    uint16(size.Width),
		Y:    uint16(size.Height),
	}
	return pty.Setsize(p.pty, ptySize)
}

// Close closes the PTY.
func (p *PTY) Close() error {
	var errs []error
	if p.tty != nil {
		if err := p.tty.Close(); err != nil {
			errs = append(errs, err)
		} else {
			p.tty = nil
		}
	}
	if p.pty != nil {
		if err := p.pty.Close(); err != nil {
			errs = append(errs, err)
		} else {
			p.pty = nil
		}
	}
	return errors.Join(errs...)
}

// StartCommand starts the given command with the PTY.
func (p *PTY) StartCommand(ctx context.Context, cmd *exec.Cmd) error {
	// This logic is based on the logic in pty.Start()

	log := klog.FromContext(ctx)

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true

	defer func() {
		if p.tty != nil {
			if err := p.tty.Close(); err != nil {
				log.Error(err, "closing tty after starting command")
			}
			p.tty = nil
		}
	}()
	if cmd.Stdout == nil {
		cmd.Stdout = p.tty
	}
	if cmd.Stderr == nil {
		cmd.Stderr = p.tty
	}
	if cmd.Stdin == nil {
		cmd.Stdin = p.tty
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	return nil
}
