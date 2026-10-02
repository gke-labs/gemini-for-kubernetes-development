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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type BlobStore struct {
	root string
}

func NewBlobStore(root string) (*BlobStore, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("failed to create blobstore root: %w", err)
	}
	return &BlobStore{root: root}, nil
}

func (b *BlobStore) Put(r io.Reader) (string, int64, error) {
	tmpFile, err := os.CreateTemp(b.root, "upload-*")
	if err != nil {
		return "", 0, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	hash := sha256.New()
	mw := io.MultiWriter(tmpFile, hash)

	size, err := io.Copy(mw, r)
	if err != nil {
		return "", 0, fmt.Errorf("failed to copy data: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		return "", 0, fmt.Errorf("failed to sync temp file: %w", err)
	}

	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	finalPath := b.path(digest)

	if err := os.MkdirAll(filepath.Dir(finalPath), 0755); err != nil {
		return "", 0, fmt.Errorf("failed to create digest dir: %w", err)
	}

	if err := os.Rename(tmpFile.Name(), finalPath); err != nil {
		return "", 0, fmt.Errorf("failed to rename to final path: %w", err)
	}

	return digest, size, nil
}

func (b *BlobStore) Get(digest string) (io.ReadCloser, error) {
	return os.Open(b.path(digest))
}

func (b *BlobStore) Exists(digest string) (bool, error) {
	_, err := os.Stat(b.path(digest))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (b *BlobStore) Stat(digest string) (os.FileInfo, error) {
	return os.Stat(b.path(digest))
}

func (b *BlobStore) path(digest string) string {
	// digest format is "sha256:<hex>"
	// We can shard it to avoid too many files in one directory
	// e.g., root/sha256/ab/cd/abcdef...
	if len(digest) < 14 || digest[:7] != "sha256:" {
		return filepath.Join(b.root, "invalid", digest)
	}
	hash := digest[7:]
	return filepath.Join(b.root, "blobs", "sha256", hash[:2], hash[2:4], hash)
}
