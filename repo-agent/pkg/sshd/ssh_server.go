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
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"
)

type Server struct {
}

func NewServer() *Server {
	return &Server{}
}

func (s *Server) Start(ctx context.Context, conn net.Conn) error {
	log := klog.FromContext(ctx)

	// TODO: Real auth
	sshServerCfg := &ssh.ServerConfig{
		NoClientAuth: true,
		NoClientAuthCallback: func(conn ssh.ConnMetadata) (*ssh.Permissions, error) {
			log.V(2).Info("NoClientAuthCallback called", "remoteAddr", conn.RemoteAddr())
			permissions := &ssh.Permissions{}
			return permissions, nil
		},
	}

	sshDir := os.Getenv("SSHD_ROOT_DIR")
	if sshDir == "" {
		sshDir = "/etc/ssh"
	}
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		if sshDir == "/etc/ssh" {
			log.Info("Could not create /etc/ssh, falling back to /tmp/ssh", "err", err)
			sshDir = "/tmp/ssh"
			if err := os.MkdirAll(sshDir, 0700); err != nil {
				return fmt.Errorf("creating ssh dir fallback %q: %w", sshDir, err)
			}
		} else {
			return fmt.Errorf("creating ssh dir %q: %w", sshDir, err)
		}
	}
	privateKeyPath := filepath.Join(sshDir, "ssh_host_ed25519_key")
	publicKeyPath := filepath.Join(sshDir, "ssh_host_ed25519_key.pub")
	privateKeyBytes, err := os.ReadFile(privateKeyPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Info("Host key file does not exist, generating new one", "path", privateKeyPath)
			publicKey, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
			if err != nil {
				return fmt.Errorf("generating new host key: %w", err)
			}
			// Write private key to file
			privateKeyPEMBlock, err := ssh.MarshalPrivateKey(privateKey, "")
			if err != nil {
				return fmt.Errorf("marshaling private key: %w", err)
			}
			privateKeyBytes = pem.EncodeToMemory(privateKeyPEMBlock)
			if err := os.WriteFile(privateKeyPath, privateKeyBytes, 0600); err != nil {
				return fmt.Errorf("writing new host key file: %w", err)
			}
			log.Info("Generated new host key", "path", privateKeyPath)

			// Write public key to file
			publicSSHKey, err := ssh.NewPublicKey(publicKey)
			if err != nil {
				return fmt.Errorf("creating ssh public key: %w", err)
			}
			publicKeyString := "ssh-ed25519" + " " + base64.StdEncoding.EncodeToString(publicSSHKey.Marshal())
			if err := os.WriteFile(publicKeyPath, []byte(publicKeyString), 0644); err != nil {
				return fmt.Errorf("writing new host public key file %q: %w", publicKeyPath, err)
			}
		} else {
			return fmt.Errorf("reading private key file: %w", err)
		}
	}

	hostKey, err := ssh.ParsePrivateKey(privateKeyBytes)
	if err != nil {
		return fmt.Errorf("parsing host key: %w", err)
	}
	sshServerCfg.AddHostKey(hostKey)
	sshConnection, newChannels, sshRequests, err := ssh.NewServerConn(conn, sshServerCfg)
	if err != nil {
		return fmt.Errorf("starting ssh server: %w", err)
	}
	defer sshConnection.Close()

	// Discard all requests
	// go ssh.DiscardRequests(sshRequests)
	go func() {
		for req := range sshRequests {
			switch req.Type {
			case "keepalive@openssh.com":
				if req.WantReply {
					if err := req.Reply(true, nil); err != nil {
						log.Error(err, "replying to keepalive request")
					}
				}

			default:
				log.Info("Received unknown SSH request", "type", req.Type)
				if req.WantReply {
					if err := req.Reply(false, nil); err != nil {
						log.Error(err, "replying to unknown request")
					}
				}
			}
		}
	}()

	// log.Info("SSH server: new connection", "user", sshConnection.User(), "remoteAddr", sshConnection.RemoteAddr())

	for newChannel := range newChannels {
		// log.Info("SSH server: new channel", "type", newChannel.ChannelType())

		switch newChannel.ChannelType() {
		case "session":
			channel, requests, err := newChannel.Accept()
			if err != nil {
				return fmt.Errorf("accepting session channel: %w", err)
			}
			go func() {
				if err := s.runSessionChannel(ctx, channel, requests); err != nil {
					log.Error(err, "running session channel")
				}
			}()
		case "direct-tcpip":
			if err := s.runDirectTCPIPChannel(ctx, newChannel); err != nil {
				log.Error(err, "running direct-tcpip channel")
			}

		default:
			if err := newChannel.Reject(ssh.UnknownChannelType, "unknown channel type"); err != nil {
				return fmt.Errorf("rejecting unknown channel type: %w", err)
			}
		}
	}

	return nil
}
