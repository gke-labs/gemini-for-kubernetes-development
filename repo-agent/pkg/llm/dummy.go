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
	"bytes"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

const (
	dummyReview = `note: |
  This is a dummy test review
review:
  body: |
    Nice work overall! Here are a few suggestions to improve the code
  comments:
    - path: nofile.go
      line: 279
      body: Rather than hardcoding these suffixes, consider making them configurable
      side: RIGHT
    - path: sdk/somefile.py
      line: 42
      body: Consider using a more efficient algorithm here
      side: RIGHT
`

	triageOutput = `/kind feature
This issue is a feature request to add AGI to the project.
`
)

var _ Provider = &Dummy{}

type Dummy struct {
	processors []PostProcessor
	ProviderConfig
}

func (d *Dummy) Setup() error {
	klog.Info("Dummy provider setup")
	return nil
}

func (d *Dummy) Cleanup() error {
	klog.Info("Dummy provider cleanup")
	return nil
}

func (d *Dummy) ExpandPrompt(prompt string) (string, error) {
	return expandCommands(prompt, ".gemini")
}

func (d *Dummy) response(prompt string) []byte {
	// if prompt contains "class DraftReviewComment(BaseModel)", return a different response
	if strings.Contains(prompt, "class DraftReviewComment(BaseModel)") {
		return []byte(dummyReview)
	}
	if bytes.Contains([]byte(prompt), []byte(`Start the response with "/kind <Category>" where`)) {
		return []byte(triageOutput)
	}
	return []byte("Response from Dummy LLM. This is a test")
}

func (d *Dummy) Run(prompt string) ([]byte, *Stats, error) {
	var err error
	klog.Infof("Dummy provider running with prompt")
	// sleep to simulate processing time
	time.Sleep(5 * time.Second)
	output := d.response(prompt)
	for _, p := range d.processors {
		output, err = p(output)
		if err != nil {
			return nil, nil, err
		}
	}
	usage := &Stats{
		Models: map[string]ModelUsage{
			"dummy": {
				API:    APIUsage{TotalRequests: 1},
				Tokens: TokenUsage{Input: 100, Output: 50, Total: 150},
			},
		},
	}
	return output, usage, nil
}

func (d *Dummy) AddPostProcessor(p PostProcessor) {
	d.processors = append(d.processors, p)
}

func (d *Dummy) QuotaCheck() bool {
	return true
}
