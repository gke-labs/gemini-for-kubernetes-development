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

package gcs

import (
	"context"
	"fmt"

	storage "cloud.google.com/go/storage"
)

type Uploader interface {
	Upload(ctx context.Context, bucket, path string, data []byte) error
}

type Client struct {
	client *storage.Client
}

func NewClient(ctx context.Context) (*Client, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{client: client}, nil
}

func (c *Client) Upload(ctx context.Context, bucket, path string, data []byte) error {
	bkt := c.client.Bucket(bucket)
	obj := bkt.Object(path)
	w := obj.NewWriter(ctx)
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return fmt.Errorf("failed to write to gcs: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("failed to close gcs writer: %w", err)
	}
	return nil
}
