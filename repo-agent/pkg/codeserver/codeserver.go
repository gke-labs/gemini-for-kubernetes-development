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

package codeserver

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
	"k8s.io/klog/v2"
)

const (
	CodeServerPort = 13337
	WorkspacePath  = "/workspaces"
)

var execCommand = exec.Command

func runDummyCommand() (*exec.Cmd, error) {
	cmd := execCommand("sleep", "infinity")
	cmd.Stdout = os.Stdout
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	klog.Infof("Running dummy command in subprocess %d\n", cmd.Process.Pid)
	return cmd, nil
}

func Start() (*exec.Cmd, error) {
	repoURL := os.Getenv("GIT_HTML_URL")
	_, repo, err := github.ParseHTMLUrl(repoURL)
	if err != nil {
		return nil, fmt.Errorf("invalid GIT_HTML_URL %q: %w", repoURL, err)
	}

	codeServerPath := "/usr/bin/code-server"
	// check if code-server exists
	if _, err := os.Stat(codeServerPath); err != nil {
		if os.IsNotExist(err) {
			// code-server not found.
			klog.Info("code-server not found, running dummy command instead")
			return runDummyCommand()
		}
		return nil, err
	}

	klog.Info("starting code-server")
	args := []string{"--auth=none", fmt.Sprintf("--bind-addr=0.0.0.0:%d", CodeServerPort), WorkspacePath + "/" + repo}
	cmd := execCommand(codeServerPath, args...)
	cmd.Stdout = os.Stdout
	err = cmd.Start()
	if err != nil {
		return nil, err
	}
	klog.Infof("Running code-server in subprocess %d\n", cmd.Process.Pid)
	return cmd, nil
}
