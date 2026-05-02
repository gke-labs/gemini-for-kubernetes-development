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

func TestCircularBuffer(t *testing.T) {
	t.Run("NewCircularBuffer", func(t *testing.T) {
		cb := NewCircularBuffer(10)
		if cb.size != 10 {
			t.Errorf("NewCircularBuffer() size = %v, want %v", cb.size, 10)
		}
		if cb.count != 0 {
			t.Errorf("NewCircularBuffer() count = %v, want %v", cb.count, 0)
		}
		if cb.write != 0 {
			t.Errorf("NewCircularBuffer() write = %v, want %v", cb.write, 0)
		}
	})

	t.Run("Write and String", func(t *testing.T) {
		cb := NewCircularBuffer(10)
		_, _ = cb.Write([]byte("hello"))
		if cb.String() != "hello" {
			t.Errorf("Write() or String() got %v, want %v", cb.String(), "hello")
		}
		_, _ = cb.Write([]byte(" world"))
		if cb.String() != "ello world" {
			t.Errorf("Write() or String() got %v, want %v", cb.String(), "ello world")
		}
	})

	t.Run("Write overflow", func(t *testing.T) {
		cb := NewCircularBuffer(10)
		_, _ = cb.Write([]byte("1234567890"))
		_, _ = cb.Write([]byte("abc"))
		if cb.String() != "4567890abc" {
			t.Errorf("Write() overflow got %v, want %v", cb.String(), "4567890abc")
		}
	})

	t.Run("Write more than size", func(t *testing.T) {
		cb := NewCircularBuffer(5)
		_, _ = cb.Write([]byte("1234567890"))
		if cb.String() != "67890" {
			t.Errorf("Write() more than size got %v, want %v", cb.String(), "67890")
		}
	})

	t.Run("Bytes", func(t *testing.T) {
		cb := NewCircularBuffer(10)
		_, _ = cb.Write([]byte("hello"))
		if string(cb.Bytes()) != "hello" {
			t.Errorf("Bytes() got %v, want %v", string(cb.Bytes()), "hello")
		}
	})

	t.Run("Empty buffer String", func(t *testing.T) {
		cb := NewCircularBuffer(10)
		if cb.String() != "" {
			t.Errorf("Empty buffer String() got %v, want empty string", cb.String())
		}
	})

	t.Run("Empty buffer Bytes", func(t *testing.T) {
		cb := NewCircularBuffer(10)
		if len(cb.Bytes()) != 0 {
			t.Errorf("Empty buffer Bytes() got %v, want empty byte slice", cb.Bytes())
		}
	})

	t.Run("Bytes overflow", func(t *testing.T) {
		cb := NewCircularBuffer(10)
		_, _ = cb.Write([]byte("1234567890"))
		_, _ = cb.Write([]byte("abc"))
		if string(cb.Bytes()) != "4567890abc" {
			t.Errorf("Bytes() overflow got %v, want %v", string(cb.Bytes()), "4567890abc")
		}
	})
}
