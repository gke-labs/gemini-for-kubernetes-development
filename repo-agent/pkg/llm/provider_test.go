// Copyright 2025 Google LLC
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
