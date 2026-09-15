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

package repowatch

import (
	"sync"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

type fakeLaunch struct {
	Key        string
	Opts       factorycli.FixOptions
	ReviewOpts *factorycli.ReviewOptions
}

// fakeLauncher records factory CLI invocations instead of exec'ing the binary.
type fakeLauncher struct {
	mu      sync.Mutex
	calls   []fakeLaunch
	running map[string]bool
	results map[string]factorycli.Result
}

func newFakeLauncher() *fakeLauncher {
	return &fakeLauncher{
		running: make(map[string]bool),
		results: make(map[string]factorycli.Result),
	}
}

func (f *fakeLauncher) StartFix(key string, opts factorycli.FixOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[key] {
		return false
	}
	f.calls = append(f.calls, fakeLaunch{Key: key, Opts: opts})
	return true
}

func (f *fakeLauncher) StartReview(key string, opts factorycli.ReviewOptions) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[key] {
		return false
	}
	f.calls = append(f.calls, fakeLaunch{Key: key, ReviewOpts: &opts})
	return true
}

func (f *fakeLauncher) IsRunning(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[key]
}

func (f *fakeLauncher) LastResult(key string) (factorycli.Result, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.results[key]
	return res, ok
}

func (f *fakeLauncher) launches() []fakeLaunch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeLaunch(nil), f.calls...)
}
