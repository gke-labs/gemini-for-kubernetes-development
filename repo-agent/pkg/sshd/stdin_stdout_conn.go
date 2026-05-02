// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sshd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// StdinStdoutConn is a net.Conn implementation that uses os.Stdin and os.Stdout
type StdinStdoutConn struct {
	stdin  *os.File
	stdout *os.File
}

var _ net.Conn = &StdinStdoutConn{}

func NewStdinStdoutConn(stdin *os.File, stdout *os.File) *StdinStdoutConn {
	return &StdinStdoutConn{
		stdin:  stdin,
		stdout: stdout,
	}
}

func (c *StdinStdoutConn) Read(b []byte) (n int, err error) {
	return c.stdin.Read(b)
}

func (c *StdinStdoutConn) Write(b []byte) (n int, err error) {
	return c.stdout.Write(b)
}
func (c *StdinStdoutConn) Close() error {
	var errs []error
	if err := c.stdin.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := c.stdout.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (c *StdinStdoutConn) LocalAddr() net.Addr {
	return &net.UnixAddr{Name: "stdin", Net: "unix"}
}

func (c *StdinStdoutConn) RemoteAddr() net.Addr {
	return &net.UnixAddr{Name: "stdout", Net: "unix"}
}

func (c *StdinStdoutConn) SetDeadline(_ time.Time) error {
	return fmt.Errorf("SetDeadline not supported")
}

func (c *StdinStdoutConn) SetReadDeadline(_ time.Time) error {
	return fmt.Errorf("SetReadDeadline not supported")
}

func (c *StdinStdoutConn) SetWriteDeadline(_ time.Time) error {
	return fmt.Errorf("SetWriteDeadline not supported")
}
