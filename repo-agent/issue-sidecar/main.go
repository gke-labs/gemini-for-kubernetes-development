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

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const (
	outputFile = "/workspaces/agent-output.txt"
)

var (
	gvr = schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
)

func main() {
	fmt.Println("starting issue sidecar")
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

	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	dc, err := dynamic.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	var last string
	for {
		time.Sleep(10 * time.Second)
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
		iss, err := dc.Resource(gvr).Namespace(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			fmt.Println("getting issuesandbox:", err)
			continue
		}

		if err := unstructured.SetNestedField(iss.Object, string(b), "status", "agentDraft"); err != nil {
			fmt.Println("setting status:", err)
			continue
		}

		if _, err := dc.Resource(gvr).Namespace(namespace).UpdateStatus(context.TODO(), iss, metav1.UpdateOptions{}); err != nil {
			fmt.Println("updating status:", err)
			continue
		}
		last = string(b)
		fmt.Println("updated crd with latest changes")
	}
}
