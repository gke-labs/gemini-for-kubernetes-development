/*
Copyright 2024 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package agentoutput

import (
	"context"
	"fmt"
	"os"
	"time"

	schema "k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	outputFile           = "/workspaces/agent-output.txt"
	sleepIntervalSeconds = 10
	AgentDraftAnnotation = "agentDraft"
)

// Run starts the sidecar watcher loop.
// componentName is used for logging (e.g., "issue", "review").
// gvr is the GroupVersionResource to update.
func Run(componentName string, gvr schema.GroupVersionResource) {
	fmt.Printf("starting %s agent output watcher\n", componentName)
	name := os.Getenv("NAME")
	if name == "" {
		fmt.Println("missing NAME env")
		os.Exit(1)
	}
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		fmt.Println("missing NAMESPACE env")
		os.Exit(1)
	}

	var last string
	for {
		time.Sleep(sleepIntervalSeconds * time.Second)
		fmt.Println("watching for file", outputFile)
		_, err := os.Stat(outputFile)
		if os.IsNotExist(err) {
			continue
		}
		b, err := os.ReadFile(outputFile)
		if err != nil {
			fmt.Println("reading file:", err)
			continue
		}
		if string(b) == last {
			continue
		}
		fmt.Println("file changed, updating crd")

		err = SetAgentDraft(context.TODO(), gvr, string(b))
		if err != nil {
			fmt.Printf("updating %s resource: %v\n", componentName, err)
			continue
		}
		last = string(b)
		fmt.Println("updated crd with latest changes")
	}
}
