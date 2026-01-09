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
	"encoding/json"
	"fmt"
	"log"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
)

func getClient() (dynamic.Interface, string, string, error) {
	name := os.Getenv("NAME")
	if name == "" {
		return nil, "", "", fmt.Errorf("missing NAME env")
	}
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		return nil, "", "", fmt.Errorf("missing NAMESPACE env")
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, "", "", err
	}

	dc, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, "", "", err
	}
	return dc, name, namespace, nil
}

// SetAgentState updates the agentState and agentStateMessage annotations.
func SetAgentState(ctx context.Context, gvr schema.GroupVersionResource, state string, message string) error {
	dc, name, namespace, err := getClient()
	if err != nil {
		return err
	}

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]string{
				"agentState":        state,
				"agentStateMessage": message,
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	log.Printf("patching resource %s/%s with: %s\n", namespace, name, patchBytes)
	_, err = dc.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		log.Printf("error patching resource %s/%s: %v\n", namespace, name, err)
		return err
	}
	return nil
}

// SetAgentLabel replaces the agentLabels annotation with the provided labels.
func SetAgentLabel(gvr schema.GroupVersionResource, labels []string) error {
	dc, name, namespace, err := getClient()
	if err != nil {
		return err
	}

	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("failed to marshal labels: %w", err)
	}

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]string{
				"agentLabels": string(labelsJSON),
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	log.Printf("patching resource %s/%s labels with: %s\n", namespace, name, patchBytes)
	_, err = dc.Resource(gvr).Namespace(namespace).Patch(context.TODO(), name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		log.Printf("error patching resource %s/%s: %v\n", namespace, name, err)
		return err
	}
	return nil
}

// AddAgentLabel adds a list of labels to the agentLabels annotation, handling deduplication.
func AddAgentLabel(gvr schema.GroupVersionResource, newLabels []string) error {
	dc, name, namespace, err := getClient()
	if err != nil {
		return err
	}

	// TODO (barney-s): switch to server-side apply when supported for dynamic client
	// Use RetryOnConflict to handle potential concurrent updates
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the current resource
		obj, err := dc.Resource(gvr).Namespace(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}

		var existingLabels []string
		if val, ok := annotations["agentLabels"]; ok {
			if err := json.Unmarshal([]byte(val), &existingLabels); err != nil {
				log.Printf("warning: failed to unmarshal existing agentLabels: %v, resetting", err)
				existingLabels = []string{}
			}
		}

		changed := false
		for _, newLabel := range newLabels {
			exists := false
			for _, l := range existingLabels {
				if l == newLabel {
					exists = true
					break
				}
			}
			if !exists {
				existingLabels = append(existingLabels, newLabel)
				changed = true
			}
		}

		if changed {
			labelsJSON, err := json.Marshal(existingLabels)
			if err != nil {
				return err
			}
			annotations["agentLabels"] = string(labelsJSON)
			obj.SetAnnotations(annotations)

			_, err = dc.Resource(gvr).Namespace(namespace).Update(context.TODO(), obj, metav1.UpdateOptions{})
			return err
		}

		return nil // No new labels to add
	})
}
