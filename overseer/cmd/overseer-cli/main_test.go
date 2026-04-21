/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestIsBot(t *testing.T) {
	tests := []struct {
		name      string
		login     string
		botLogin  string
		userLogin string
		expected  bool
	}{
		{
			name:     "exact match bot",
			login:    "my-bot",
			botLogin: "my-bot",
			expected: true,
		},
		{
			name:     "case insensitive bot",
			login:    "My-Bot",
			botLogin: "my-bot",
			expected: true,
		},
		{
			name:     "bot suffix match",
			login:    "my-bot[bot]",
			botLogin: "my-bot",
			expected: true,
		},
		{
			name:     "bot suffix match botLogin with suffix",
			login:    "my-bot[bot]",
			botLogin: "my-bot[bot]",
			expected: true,
		},
		{
			name:      "user match",
			login:     "my-user",
			userLogin: "my-user",
			expected:  true,
		},
		{
			name:     "no match",
			login:    "other",
			botLogin: "my-bot",
			expected: false,
		},
		{
			name:      "case insensitive user",
			login:     "My-User",
			userLogin: "my-user",
			expected:  true,
		},
		{
			name:     "bot match with suffix in botLogin only",
			login:    "my-bot",
			botLogin: "my-bot[bot]",
			expected: true,
		},
		{
			name:     "custom bot suffix match",
			login:    "my-bot[bot]-test",
			botLogin: "my-bot",
			expected: true,
		},
		{
			name:     "another custom bot suffix match",
			login:    "my-bot[bot]-app-123",
			botLogin: "my-bot",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBot(tt.login, tt.botLogin, tt.userLogin); got != tt.expected {
				t.Errorf("isBot(%q, %q, %q) = %v, want %v", tt.login, tt.botLogin, tt.userLogin, got, tt.expected)
			}
		})
	}
}

func TestIsGitHubTransient(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "rate limit error (403)",
			err:      &githubv39.ErrorResponse{Response: &http.Response{StatusCode: 403}, Message: "API rate limit exceeded"},
			expected: true,
		},
		{
			name:     "permission error (403)",
			err:      &githubv39.ErrorResponse{Response: &http.Response{StatusCode: 403}, Message: "Permission denied"},
			expected: false,
		},

		{
			name:     "rate limit error (429)",
			err:      &githubv39.ErrorResponse{Response: &http.Response{StatusCode: 429}},
			expected: true,
		},
		{
			name:     "server error (500)",
			err:      &githubv39.ErrorResponse{Response: &http.Response{StatusCode: 500}},
			expected: true,
		},
		{
			name:     "server error (503)",
			err:      &githubv39.ErrorResponse{Response: &http.Response{StatusCode: 503}},
			expected: true,
		},
		{
			name:     "not found (404)",
			err:      &githubv39.ErrorResponse{Response: &http.Response{StatusCode: 404}},
			expected: false,
		},
		{
			name:     "i/o timeout",
			err:      errors.New("i/o timeout"),
			expected: true,
		},
		{
			name:     "tls handshake timeout",
			err:      errors.New("net/http: TLS handshake timeout"),
			expected: true,
		},
		{
			name:     "connection reset",
			err:      syscall.ECONNRESET,
			expected: true,
		},
		{
			name:     "connection refused",
			err:      &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			expected: true,
		},
		{
			name:     "context deadline exceeded",
			err:      context.DeadlineExceeded,
			expected: true,
		},
		{
			name:     "EOF error",
			err:      io.EOF,
			expected: true,
		},
		{
			name:     "unexpected EOF error",
			err:      io.ErrUnexpectedEOF,
			expected: true,
		},
		{
			name:     "generic timeout should NOT match anymore",
			err:      errors.New("timeout"),
			expected: false,
		},
		{
			name:     "other error",
			err:      errors.New("something went wrong"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGitHubTransient(tt.err); got != tt.expected {
				t.Errorf("isGitHubTransient(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}
