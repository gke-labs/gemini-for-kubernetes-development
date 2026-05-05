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
	"fmt"
	"testing"
)

func TestStripYAMLMarkers(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		want    []byte
		wantErr bool
	}{
		{
			name:    "with markers",
			input:   []byte("```yaml\nfoo: bar\n```"),
			want:    []byte("foo: bar"),
			wantErr: false,
		},
		{
			name:    "without markers",
			input:   []byte("foo: bar"),
			want:    []byte("foo: bar"),
			wantErr: false,
		},
		{
			name:    "empty input",
			input:   []byte(""),
			want:    []byte(""),
			wantErr: false,
		},
		{
			name:    "only markers",
			input:   []byte("```yaml\n```"),
			want:    []byte(""),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := StripYAMLMarkers(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("StripYAMLMarkers() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf("StripYAMLMarkers() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewProvider(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		wantErr      bool
		expectedType string
	}{
		{
			name:         "gemini-cli provider",
			provider:     "gemini-cli",
			wantErr:      false,
			expectedType: "*llm.Gemini",
		},
		{
			name:     "unknown provider",
			provider: "unknown",
			wantErr:  true,
		},
		{
			name:         "claude provider",
			provider:     "claude",
			wantErr:      false,
			expectedType: "*llm.Claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewLLMProvider(ProviderConfig{Name: tt.provider})
			if (err != nil) != tt.wantErr {
				t.Errorf("NewProvider() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				providerType := fmt.Sprintf("%T", provider)
				if providerType != tt.expectedType {
					t.Errorf("NewProvider() type = %v, want %v", providerType, tt.expectedType)
				}
			}
		})
	}
}
