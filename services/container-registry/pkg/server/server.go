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

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/gke-labs/gemini-for-kubernetes-development/services/container-registry/pkg/blobstore"
)

type Server struct {
	blobs     *blobstore.BlobStore
	root      string
	uploads   map[string]string // uuid -> tempFilePath
	uploadsMu sync.Mutex
}

func NewServer(root string) (*Server, error) {
	bs, err := blobstore.NewBlobStore(filepath.Join(root, "blobs"))
	if err != nil {
		return nil, err
	}
	return &Server{
		blobs:   bs,
		root:    root,
		uploads: make(map[string]string),
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", s.handleV2)
	
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleV2(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	if path == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	// name is everything until "blobs" or "manifests"
	var name string
	var resource string
	for i, part := range parts {
		if part == "blobs" || part == "manifests" {
			name = strings.Join(parts[:i], "/")
			resource = strings.Join(parts[i:], "/")
			break
		}
	}

	if name == "" {
		http.NotFound(w, r)
		return
	}

	if strings.HasPrefix(resource, "blobs/uploads") {
		s.handleBlobUpload(name, w, r)
		return
	}

	if strings.HasPrefix(resource, "blobs/") {
		s.handleBlob(name, w, r)
		return
	}

	if strings.HasPrefix(resource, "manifests/") {
		s.handleManifest(name, w, r)
		return
	}

	if resource == "tags/list" {
		s.handleTags(name, w, r)
		return
	}

	http.NotFound(w, r)
}

func (s *Server) handleTags(name string, w http.ResponseWriter, r *http.Request) {
	manifestDir := filepath.Join(s.root, "manifests", name)
	entries, err := os.ReadDir(manifestDir)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	var tags []string
	for _, entry := range entries {
		if !entry.IsDir() {
			// Basic heuristic: if it doesn't look like a digest, it's a tag
			if !strings.HasPrefix(entry.Name(), "sha256:") {
				tags = append(tags, entry.Name())
			}
		}
	}

	resp := struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}{
		Name: name,
		Tags: tags,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleBlobUpload(name string, w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v2/"+name+"/blobs/uploads/"), "/")
	
	switch r.Method {
	case http.MethodPost:
		// Start upload
		id := uuid.New().String()
		tmpFile, err := os.CreateTemp("", "reg-upload-*")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmpFile.Close()

		s.uploadsMu.Lock()
		s.uploads[id] = tmpFile.Name()
		s.uploadsMu.Unlock()

		w.Header().Set("Location", "/v2/"+name+"/blobs/uploads/"+id)
		w.Header().Set("Range", "0-0")
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPatch:
		// Upload chunk
		if len(parts) < 1 {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		s.uploadsMu.Lock()
		tmpPath, ok := s.uploads[id]
		s.uploadsMu.Unlock()

		if !ok {
			http.NotFound(w, r)
			return
		}

		f, err := os.OpenFile(tmpPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()

		if _, err := io.Copy(f, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		fi, _ := f.Stat()
		w.Header().Set("Location", "/v2/"+name+"/blobs/uploads/"+id)
		w.Header().Set("Range", fmt.Sprintf("0-%d", fi.Size()-1))
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPut:
		// Finish upload
		if len(parts) < 1 {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		digest := r.URL.Query().Get("digest")
		if digest == "" {
			http.Error(w, "missing digest", http.StatusBadRequest)
			return
		}

		s.uploadsMu.Lock()
		tmpPath, ok := s.uploads[id]
		delete(s.uploads, id)
		s.uploadsMu.Unlock()

		if !ok {
			http.NotFound(w, r)
			return
		}

		f, err := os.Open(tmpPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		defer os.Remove(tmpPath)

		actualDigest, _, err := s.blobs.Put(f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if actualDigest != digest {
			http.Error(w, fmt.Sprintf("digest mismatch: expected %s, got %s", digest, actualDigest), http.StatusBadRequest)
			return
		}

		w.Header().Set("Location", "/v2/"+name+"/blobs/"+digest)
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBlob(name string, w http.ResponseWriter, r *http.Request) {
	digest := strings.TrimPrefix(r.URL.Path, "/v2/"+name+"/blobs/")
	
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		exists, err := s.blobs.Exists(digest)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !exists {
			http.NotFound(w, r)
			return
		}

		fi, err := s.blobs.Stat(digest)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Type", "application/octet-stream")

		if r.Method == http.MethodGet {
			f, err := s.blobs.Get(digest)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer f.Close()
			io.Copy(w, f)
		} else {
			w.WriteHeader(http.StatusOK)
		}

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleManifest(name string, w http.ResponseWriter, r *http.Request) {
	reference := strings.TrimPrefix(r.URL.Path, "/v2/"+name+"/manifests/")
	manifestPath := filepath.Join(s.root, "manifests", name, reference)

	switch r.Method {
	case http.MethodPut:
		if err := os.MkdirAll(filepath.Dir(manifestPath), 0755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := os.WriteFile(manifestPath, data, 0644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Calculate digest
		hash := sha256.New()
		hash.Write(data)
		digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))

		// Also store by digest
		digestPath := filepath.Join(s.root, "manifests", name, digest)
		if err := os.WriteFile(digestPath, data, 0644); err != nil {
			log.Printf("Failed to store manifest by digest: %v", err)
		}

		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)

	case http.MethodGet, http.MethodHead:
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				http.NotFound(w, r)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		// Try to detect content type from manifest data
		var manifest struct {
			MediaType string `json:"mediaType"`
		}
		json.Unmarshal(data, &manifest)
		if manifest.MediaType != "" {
			w.Header().Set("Content-Type", manifest.MediaType)
		} else {
			// fallback
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		}

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		
		if r.Method == http.MethodGet {
			w.Write(data)
		} else {
			w.WriteHeader(http.StatusOK)
		}

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
