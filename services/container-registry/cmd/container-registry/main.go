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

package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/services/container-registry/pkg/server"
)

func main() {
	storageRoot := os.Getenv("STORAGE_ROOT")
	if storageRoot == "" {
		storageRoot = "/data"
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":5000"
	}

	s, err := server.NewServer(storageRoot)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	log.Printf("Starting container registry on %s, storing data in %s", addr, storageRoot)
	if err := http.ListenAndServe(addr, s.Handler()); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
