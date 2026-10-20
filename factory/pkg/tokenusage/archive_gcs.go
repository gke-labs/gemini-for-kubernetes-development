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

package tokenusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2/google"
)

// GCSArchive is an Archive in a GCS bucket, under a prefix, spoken to over
// the JSON API with the pod's default credentials (Workload Identity in
// the cluster). It needs roles/storage.objectAdmin on the bucket: the
// flusher creates, reads, lists and deletes.
type GCSArchive struct {
	client *http.Client
	bucket string
	prefix string
	// endpoint is the API's base URL; tests point it at a fake.
	endpoint string
}

const gcsScope = "https://www.googleapis.com/auth/devstorage.read_write"

// NewGCSArchive returns the archive at gs://<bucket>/<prefix>.
func NewGCSArchive(ctx context.Context, bucket, prefix string) (*GCSArchive, error) {
	client, err := google.DefaultClient(ctx, gcsScope)
	if err != nil {
		return nil, fmt.Errorf("GCS credentials: %w", err)
	}
	return newGCSArchive(client, "https://storage.googleapis.com", bucket, prefix), nil
}

func newGCSArchive(client *http.Client, endpoint, bucket, prefix string) *GCSArchive {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &GCSArchive{client: client, endpoint: endpoint, bucket: bucket, prefix: prefix}
}

func (g *GCSArchive) objectURL(name string) string {
	return fmt.Sprintf("%s/storage/v1/b/%s/o/%s", g.endpoint, url.PathEscape(g.bucket), url.PathEscape(g.prefix+name))
}

func (g *GCSArchive) List(ctx context.Context, prefix string) ([]string, error) {
	var names []string
	pageToken := ""
	for {
		q := url.Values{"prefix": {g.prefix + prefix}, "fields": {"items(name),nextPageToken"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		u := fmt.Sprintf("%s/storage/v1/b/%s/o?%s", g.endpoint, url.PathEscape(g.bucket), q.Encode())
		body, err := g.do(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items         []struct{ Name string } `json:"items"`
			NextPageToken string                  `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("listing gs://%s/%s: %w", g.bucket, g.prefix+prefix, err)
		}
		for _, it := range page.Items {
			names = append(names, strings.TrimPrefix(it.Name, g.prefix))
		}
		if page.NextPageToken == "" {
			return names, nil
		}
		pageToken = page.NextPageToken
	}
}

func (g *GCSArchive) Read(ctx context.Context, name string) ([]byte, error) {
	return g.do(ctx, http.MethodGet, g.objectURL(name)+"?alt=media", nil)
}

func (g *GCSArchive) Write(ctx context.Context, name string, data []byte) error {
	q := url.Values{"uploadType": {"media"}, "name": {g.prefix + name}}
	u := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?%s", g.endpoint, url.PathEscape(g.bucket), q.Encode())
	_, err := g.do(ctx, http.MethodPost, u, data)
	return err
}

func (g *GCSArchive) Delete(ctx context.Context, name string) error {
	_, err := g.do(ctx, http.MethodDelete, g.objectURL(name), nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (g *GCSArchive) do(ctx context.Context, method, u string, body []byte) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s %s: %w", method, req.URL.Path, ErrNotFound)
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("%s %s: %s: %s", method, req.URL.Path, resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}
