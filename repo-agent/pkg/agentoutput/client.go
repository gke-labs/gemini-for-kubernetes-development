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
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
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

	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, "", "", err
	}
	return kube.DynamicClient, name, namespace, nil
}

// SetAgentState updates the agentState and agentStateMessage annotations.
func SetAgentState(ctx context.Context, gvr schema.GroupVersionResource, state string, message string) error {
	log := klog.FromContext(ctx)
	dc, name, namespace, err := getClient()
	if err != nil {
		return err
	}

	obj, err := dc.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	applyObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": obj.GetAPIVersion(),
			"kind":       obj.GetKind(),
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"annotations": map[string]string{
					"agentState":        state,
					"agentStateMessage": message,
				},
			},
		},
	}

	log.Info("applying resource with state", "namespace", namespace, "name", name, "state", state)
	_, err = dc.Resource(gvr).Namespace(namespace).Apply(ctx, name, applyObj, metav1.ApplyOptions{FieldManager: "agent-draft-client", Force: true})
	if err != nil {
		log.Info("error applying resource", "namespace", namespace, "name", name, "err", err)
		return err
	}
	return nil
}

// SetAgentDraft updates the agentDraft annotation.
func SetAgentDraft(ctx context.Context, gvr schema.GroupVersionResource, draft string) error {
	log := klog.FromContext(ctx)
	dc, name, namespace, err := getClient()
	if err != nil {
		return err
	}

	obj, err := dc.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	applyObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": obj.GetAPIVersion(),
			"kind":       obj.GetKind(),
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"annotations": map[string]string{
					"agentDraft": draft,
				},
			},
		},
	}

	log.Info("applying resource with draft", "namespace", namespace, "name", name)
	_, err = dc.Resource(gvr).Namespace(namespace).Apply(ctx, name, applyObj, metav1.ApplyOptions{FieldManager: "agent-output-client", Force: true})
	if err != nil {
		log.Info("error applying resource", "namespace", namespace, "name", name, "err", err)
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

	obj, err := dc.Resource(gvr).Namespace(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("failed to marshal labels: %w", err)
	}

	applyObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": obj.GetAPIVersion(),
			"kind":       obj.GetKind(),
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"annotations": map[string]string{
					"agentLabels": string(labelsJSON),
				},
			},
		},
	}

	klog.Infof("applying resource %s/%s labels\n", namespace, name)
	_, err = dc.Resource(gvr).Namespace(namespace).Apply(context.TODO(), name, applyObj, metav1.ApplyOptions{FieldManager: "agent-label-client"})
	if err != nil {
		klog.Infof("error applying resource %s/%s: %v\n", namespace, name, err)
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
			klog.Infof("warning: failed to unmarshal existing agentLabels: %v, resetting", err)
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

		applyObj := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": obj.GetAPIVersion(),
				"kind":       obj.GetKind(),
				"metadata": map[string]interface{}{
					"name":      name,
					"namespace": namespace,
					"annotations": map[string]string{
						"agentLabels": string(labelsJSON),
					},
				},
			},
		}

		_, err = dc.Resource(gvr).Namespace(namespace).Apply(context.TODO(), name, applyObj, metav1.ApplyOptions{FieldManager: "agent-label-client"})
		klog.Infof("added labels to resource %s/%s: %v\n", namespace, name, newLabels)
		return err
	}

	return nil // No new labels to add
}
