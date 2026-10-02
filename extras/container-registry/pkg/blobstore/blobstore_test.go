/*
Copyright 2026 The Kubernetes Authors.

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

package blobstore

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestBlobStore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "blobstore-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	bs, err := NewBlobStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("hello world")
	digest, size, err := bs.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	expectedDigest := "sha256:b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if digest != expectedDigest {
		t.Errorf("expected digest %s, got %s", expectedDigest, digest)
	}
	if size != int64(len(data)) {
		t.Errorf("expected size %d, got %d", len(data), size)
	}

	exists, err := bs.Exists(digest)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("expected blob to exist")
	}

	r, err := bs.Get(digest)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	readData, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, readData) {
		t.Errorf("expected data %s, got %s", data, readData)
	}
}
