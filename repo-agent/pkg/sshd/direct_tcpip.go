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
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"
)

type directTCPIPExtraData struct {
	HostToConnect  string
	PortToConnect  uint32
	OriginatorIP   string
	OriginatorPort uint32
}

func (d *directTCPIPExtraData) Parse(b []byte) error {
	payload := sshPayload{b: b}
	var err error
	d.HostToConnect, err = payload.PopString()
	if err != nil {
		return fmt.Errorf("parsing host to connect: %w", err)
	}
	d.PortToConnect, err = payload.PopUint32()
	if err != nil {
		return fmt.Errorf("parsing port to connect: %w", err)
	}
	d.OriginatorIP, err = payload.PopString()
	if err != nil {
		return fmt.Errorf("parsing originator IP: %w", err)
	}
	d.OriginatorPort, err = payload.PopUint32()
	if err != nil {
		return fmt.Errorf("parsing originator port: %w", err)
	}
	return nil
}

func (s *Server) runDirectTCPIPChannel(ctx context.Context, newChannel ssh.NewChannel) error {
	log := klog.FromContext(ctx)

	data := &directTCPIPExtraData{}
	if err := data.Parse(newChannel.ExtraData()); err != nil {
		if err := newChannel.Reject(ssh.UnknownChannelType, "payload parsing failed"); err != nil {
			return fmt.Errorf("rejecting direct-tcpip channel: %w", err)
		}
		return fmt.Errorf("parsing direct-tcpip extra data: %w", err)
	}

	// The contract (seems to be) that we dial the target address before deciding whether to accept/reject the connection
	targetAddr := net.JoinHostPort(data.HostToConnect, fmt.Sprintf("%d", data.PortToConnect))
	targetConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		if err := newChannel.Reject(ssh.ConnectionFailed, "dialing target address failed"); err != nil {
			return fmt.Errorf("rejecting direct-tcpip channel: %w", err)
		}
		return fmt.Errorf("dialing target address %q: %w", targetAddr, err)
	}

	sshChannel, requests, err := newChannel.Accept()
	if err != nil {
		targetConn.Close()
		return fmt.Errorf("accepting direct-tcpip channel: %w", err)
	}

	go func() {
		errChan := make(chan error, 2)
		// Start copying data between sshChannel and targetConn
		go func() {
			defer targetConn.Close()
			_, err := io.Copy(targetConn, sshChannel)
			if err != nil {
				errChan <- fmt.Errorf("copying from sshChannel to targetConn: %w", err)
			} else {
				errChan <- nil
			}
		}()
		go func() {
			defer sshChannel.Close()
			_, err := io.Copy(sshChannel, targetConn)
			if err != nil {
				errChan <- fmt.Errorf("copying from targetConn to sshChannel: %w", err)
			} else {
				errChan <- nil
			}
		}()

		go ssh.DiscardRequests(requests)

		err1 := <-errChan
		err2 := <-errChan

		errs := errors.Join(err1, err2)
		if errs != nil {
			log.Error(errs, "direct-tcpip channel copy error")
		}
	}()

	return nil
}
