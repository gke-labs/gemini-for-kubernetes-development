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
	"sync"
)

// CircularBuffer is a simple thread-safe, fixed-size circular buffer that implements io.Writer.
type CircularBuffer struct {
	mu    sync.Mutex // May not be needed if only used in single goroutine
	data  []byte
	size  int
	write int // write index
	count int // number of bytes currently in buffer
}

// NewCircularBuffer creates a new CircularBuffer with a given size.
func NewCircularBuffer(size int) *CircularBuffer {
	return &CircularBuffer{
		data: make([]byte, size),
		size: size,
	}
}

// Write writes p to the buffer, overwriting old data if the buffer is full.
func (b *CircularBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n = len(p)
	if n > b.size {
		p = p[n-b.size:]
		n = b.size
	}

	if b.write+len(p) <= b.size {
		copy(b.data[b.write:], p)
		b.write += len(p)
	} else {
		remain := b.size - b.write
		copy(b.data[b.write:], p[:remain])
		copy(b.data[0:], p[remain:])
		b.write = len(p) - remain
	}

	if b.count < b.size {
		b.count += n
		if b.count > b.size {
			b.count = b.size
		}
	}
	return len(p), nil
}

// String returns the contents of the buffer as a string, starting from the oldest data.
func (b *CircularBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		return ""
	}

	if b.write < b.size && b.count == b.size {
		buf := make([]byte, b.size)
		copy(buf, b.data[b.write:])
		copy(buf[b.size-b.write:], b.data[:b.write])
		return string(buf)
	}
	return string(b.data[:b.count])
}

// Bytes returns the contents of the buffer starting from the oldest data.
func (b *CircularBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		return []byte{}
	}

	if b.write < b.size && b.count == b.size {
		buf := make([]byte, b.size)
		copy(buf, b.data[b.write:])
		copy(buf[b.size-b.write:], b.data[:b.write])
		return buf
	}
	return b.data[:b.count]
}
