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

package llm

import (
	"testing"
)

func TestDummy_Run(t *testing.T) {
	d := &Dummy{}
	prompt := "hello"
	output, usage, err := d.Run(prompt)
	if err != nil {
		t.Fatalf("Dummy.Run failed: %v", err)
	}
	expected := "Response from Dummy LLM. This is a test"
	if string(output) != expected {
		t.Errorf("expected %q, got %q", expected, string(output))
	}
	if usage == nil {
		t.Fatal("Expected non-nil usage from Dummy provider")
	}
	if len(usage.Models) != 1 {
		t.Fatalf("Expected 1 model in usage, got %d", len(usage.Models))
	}
	if _, ok := usage.Models["dummy"]; !ok {
		t.Error("Expected usage for model 'dummy'")
	}
}

func TestDummy_Setup(t *testing.T) {
	d := &Dummy{}
	if err := d.Setup(); err != nil {
		t.Fatalf("Dummy.Setup failed: %v", err)
	}
}

func TestDummy_Cleanup(t *testing.T) {
	d := &Dummy{}
	if err := d.Cleanup(); err != nil {
		t.Fatalf("Dummy.Cleanup failed: %v", err)
	}
}

func TestDummy_ExpandPrompt(t *testing.T) {
	d := &Dummy{}
	prompt := "hello"
	expanded, err := d.ExpandPrompt(prompt)
	if err != nil {
		t.Fatalf("Dummy.ExpandPrompt failed: %v", err)
	}
	if expanded != prompt {
		t.Errorf("expected %q, got %q", prompt, expanded)
	}
}
