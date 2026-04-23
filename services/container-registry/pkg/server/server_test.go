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

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestV2Base(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "registry-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewServer(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "/v2/", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	if version := rr.Header().Get("Docker-Distribution-API-Version"); version != "registry/2.0" {
		t.Errorf("handler returned wrong version: got %v want %v", version, "registry/2.0")
	}
}

func TestBlobUpload(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "registry-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewServer(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	name := "test-repo"

	// 1. Initiate upload
	req, _ := http.NewRequest("POST", "/v2/"+name+"/blobs/uploads/", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusAccepted {
		t.Fatalf("Expected 202, got %v", status)
	}
	location := rr.Header().Get("Location")
	if location == "" {
		t.Fatal("Expected Location header")
	}

	// 2. Upload data
	data := []byte("hello world")
	req, _ = http.NewRequest("PATCH", location, bytes.NewReader(data))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusAccepted {
		t.Fatalf("Expected 202, got %v", status)
	}

	// 3. Finish upload
	digest := "sha256:b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	req, _ = http.NewRequest("PUT", location+"?digest="+digest, nil)
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusCreated {
		t.Fatalf("Expected 201, got %v", status)
	}

	// 4. Verify blob
	req, _ = http.NewRequest("GET", "/v2/"+name+"/blobs/"+digest, nil)
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("Expected 200, got %v", status)
	}
	if !bytes.Equal(rr.Body.Bytes(), data) {
		t.Errorf("Expected %s, got %s", data, rr.Body.Bytes())
	}
}
