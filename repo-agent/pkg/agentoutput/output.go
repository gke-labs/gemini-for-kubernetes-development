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

const (
        // AgentDraftAnnotation is the key for the annotation where the agent writes its draft response.
        // The UI watches this annotation to provide real-time streaming feedback to the user.
        AgentDraftAnnotation     = "agentDraft"
        // AgentDraftTypeAnnotation specifies the type/format of the draft content (e.g., markdown, code).
        AgentDraftTypeAnnotation = "agentDraftType"
)
// AgentOutputConfig holds configuration for the agent output client.
type AgentOutput struct {
	name          string
	namespace     string
	gvr           schema.GroupVersionResource
	dynamicClient dynamic.Interface
}

// New creates a new AgentOutput instance.
func New(gvr schema.GroupVersionResource, name, namespace string) (*AgentOutput, error) {
	if name == "" {
		name = os.Getenv("NAME")
		if name == "" {
			return nil, fmt.Errorf("missing NAME env")
		}
	}

	if namespace == "" {
		namespace = os.Getenv("NAMESPACE")
		if namespace == "" {
			return nil, fmt.Errorf("missing NAMESPACE env")
		}
	}

	ao := &AgentOutput{
		name:      name,
		namespace: namespace,
		gvr:       gvr,
	}

	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, err
	}

	ao.dynamicClient = kube.DynamicClient

	return ao, nil
}

// SetAgentState updates the agentState and agentStateMessage annotations.
// These annotations are watched by the UI to display the current status of the agent (e.g., "Thinking", "Writing Code")
// and any relevant status messages to the user.
func (ao *AgentOutput) SetAgentState(ctx context.Context, state string, message string) error {
	log := klog.FromContext(ctx)
	obj, err := ao.dynamicClient.Resource(ao.gvr).Namespace(ao.namespace).Get(ctx, ao.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	applyObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": obj.GetAPIVersion(),
			"kind":       obj.GetKind(),
			"metadata": map[string]interface{}{
				"name":      ao.name,
				"namespace": ao.namespace,
				"annotations": map[string]string{
					"agentState":        state,
					"agentStateMessage": message,
				},
			},
		},
	}

	log.Info("applying resource with state", "namespace", ao.namespace, "name", ao.name, "state", state)
	_, err = ao.dynamicClient.Resource(ao.gvr).Namespace(ao.namespace).Apply(ctx, ao.name, applyObj, metav1.ApplyOptions{FieldManager: "agent-draft-client", Force: true})
	if err != nil {
		log.Info("error applying resource", "namespace", ao.namespace, "name", ao.name, "err", err)
		return err
	}
	return nil
}

// SetAgentDraft updates the agentDraft annotation.
func (ao *AgentOutput) SetAgentDraft(ctx context.Context, draft string) error {
	log := klog.FromContext(ctx)
	obj, err := ao.dynamicClient.Resource(ao.gvr).Namespace(ao.namespace).Get(ctx, ao.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	applyObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": obj.GetAPIVersion(),
			"kind":       obj.GetKind(),
			"metadata": map[string]interface{}{
				"name":      ao.name,
				"namespace": ao.namespace,
				"annotations": map[string]string{
					"agentDraft": draft,
				},
			},
		},
	}

	log.Info("applying resource with draft", "namespace", ao.namespace, "name", ao.name)
	_, err = ao.dynamicClient.Resource(ao.gvr).Namespace(ao.namespace).Apply(ctx, ao.name, applyObj, metav1.ApplyOptions{FieldManager: "agent-output-client", Force: true})
	if err != nil {
		log.Info("error applying resource", "namespace", ao.namespace, "name", ao.name, "err", err)
		return err
	}
	return nil
}

// SetAgentDraftType updates the agentDraftType annotation.
func (ao *AgentOutput) SetAgentDraftType(ctx context.Context, draftType string) error {
	log := klog.FromContext(ctx)
	obj, err := ao.dynamicClient.Resource(ao.gvr).Namespace(ao.namespace).Get(ctx, ao.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	applyObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": obj.GetAPIVersion(),
			"kind":       obj.GetKind(),
			"metadata": map[string]interface{}{
				"name":      ao.name,
				"namespace": ao.namespace,
				"annotations": map[string]string{
					"agentDraftType": draftType,
				},
			},
		},
	}

	log.Info("applying resource with draft type", "namespace", ao.namespace, "name", ao.name, "type", draftType)
	_, err = ao.dynamicClient.Resource(ao.gvr).Namespace(ao.namespace).Apply(ctx, ao.name, applyObj, metav1.ApplyOptions{FieldManager: "agent-output-type-client", Force: true})
	if err != nil {
		log.Info("error applying resource", "namespace", ao.namespace, "name", ao.name, "err", err)
		return err
	}
	return nil
}

// SetAgentLabel replaces the agentLabels annotation with the provided labels.
func (ao *AgentOutput) SetAgentLabel(ctx context.Context, labels []string) error {
	obj, err := ao.dynamicClient.Resource(ao.gvr).Namespace(ao.namespace).Get(ctx, ao.name, metav1.GetOptions{})
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
				"name":      ao.name,
				"namespace": ao.namespace,
				"annotations": map[string]string{
					"agentLabels": string(labelsJSON),
				},
			},
		},
	}

	klog.Infof("applying resource %s/%s labels\n", ao.namespace, ao.name)
	_, err = ao.dynamicClient.Resource(ao.gvr).Namespace(ao.namespace).Apply(ctx, ao.name, applyObj, metav1.ApplyOptions{FieldManager: "agent-label-client"})
	if err != nil {
		klog.Infof("error applying resource %s/%s: %v\n", ao.namespace, ao.name, err)
		return err
	}
	return nil
}

// AddAgentLabel adds a list of labels to the agentLabels annotation, handling deduplication.
func (ao *AgentOutput) AddAgentLabel(ctx context.Context, newLabels []string) error {
	obj, err := ao.dynamicClient.Resource(ao.gvr).Namespace(ao.namespace).Get(ctx, ao.name, metav1.GetOptions{})
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
					"name":      ao.name,
					"namespace": ao.namespace,
					"annotations": map[string]string{
						"agentLabels": string(labelsJSON),
					},
				},
			},
		}

		_, err = ao.dynamicClient.Resource(ao.gvr).Namespace(ao.namespace).Apply(ctx, ao.name, applyObj, metav1.ApplyOptions{FieldManager: "agent-label-client"})
		klog.Infof("added labels to resource %s/%s: %v\n", ao.namespace, ao.name, newLabels)
		return err
	}

	return nil // No new labels to add
}
