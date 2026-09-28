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

package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
)

// Filing a click.
//
// Every kickoff endpoint used to read the board, merge one entry into a
// JSON map in an annotation, and write the board back. Four handlers
// doing that against one object, with no conflict retry between them,
// meant two members clicking within the same second lost one of the
// clicks — silently, because the write succeeded, just without the other
// entry in it.
//
// A click is an object now. Creates do not collide, so there is nothing
// to retry; the parameters are typed fields the API server validates
// rather than a pipe-delimited string; and the controller writes the
// outcome back where whoever clicked can read it.

var requestGVR = schema.GroupVersionResource{
	Group:    "board.gemini.google.com",
	Version:  "v1alpha1",
	Resource: "requests",
}

// fileRequest records one click against a board, and returns the
// Request that now stands for it.
//
// Clicking the same thing twice is one request, not two: the second call
// finds the first still standing and returns it untouched. That is the
// behaviour the annotation map gave for free (same key, same value) and
// it is what stops a double-click from starting two deploys.
func (s *Server) fileRequest(ctx context.Context, board *unstructured.Unstructured, spec boardv1alpha1.RequestSpec) (*boardv1alpha1.Request, error) {
	spec.Board = board.GetName()
	if existing, err := s.findActiveRequest(ctx, board.GetNamespace(), spec); err == nil && existing != nil {
		return existing, nil
	}

	req := &boardv1alpha1.Request{
		ObjectMeta: v1.ObjectMeta{
			GenerateName: requestPrefix(spec),
			Namespace:    board.GetNamespace(),
			Labels: map[string]string{
				boardv1alpha1.LabelBoard: board.GetName(),
				boardv1alpha1.LabelVerb:  spec.Verb,
			},
			Annotations: map[string]string{
				boardv1alpha1.AnnotationSubject: spec.Subject(),
			},
			// Owned by the board, so deleting a board takes its
			// outstanding clicks with it rather than leaving the
			// controller launching work for a queue that is gone.
			OwnerReferences: []v1.OwnerReference{{
				APIVersion: boardv1alpha1.GroupVersion.String(),
				Kind:       "RepoBoard",
				Name:       board.GetName(),
				UID:        board.GetUID(),
			}},
		},
		Spec: spec,
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(req)
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}
	// The converter emits an empty status block; the subresource would
	// drop it anyway, and sending it invites a diff nobody wrote.
	delete(obj, "status")
	obj["apiVersion"] = boardv1alpha1.GroupVersion.String()
	obj["kind"] = "Request"

	created, err := s.K8sManager.Client.Resource(requestGVR).
		Namespace(board.GetNamespace()).
		Create(ctx, &unstructured.Unstructured{Object: obj}, v1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return requestFrom(created)
}

// requestPrefix is the generateName. It is a label for a human reading
// `kubectl get requests`, not an identity — the API server appends the
// identity.
func requestPrefix(spec boardv1alpha1.RequestSpec) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, spec.Subject())
	if len(slug) > 24 {
		slug = slug[:24]
	}
	return strings.Trim(spec.Verb+"-"+slug, "-") + "-"
}

// findActiveRequest looks for a standing click on the same subject.
func (s *Server) findActiveRequest(ctx context.Context, namespace string, spec boardv1alpha1.RequestSpec) (*boardv1alpha1.Request, error) {
	list, err := s.listRequests(ctx, namespace, v1.ListOptions{
		LabelSelector: boardv1alpha1.LabelBoard + "=" + spec.Board + "," + boardv1alpha1.LabelVerb + "=" + spec.Verb,
	})
	if err != nil {
		return nil, err
	}
	key := spec.Key()
	for i := range list {
		if list[i].Active() && list[i].Spec.Key() == key {
			return &list[i], nil
		}
	}
	return nil, nil
}

// listRequests reads Requests in one namespace, newest first.
func (s *Server) listRequests(ctx context.Context, namespace string, opts v1.ListOptions) ([]boardv1alpha1.Request, error) {
	raw, err := s.K8sManager.Client.Resource(requestGVR).Namespace(namespace).List(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make([]boardv1alpha1.Request, 0, len(raw.Items))
	for i := range raw.Items {
		req, err := requestFrom(&raw.Items[i])
		if err != nil {
			continue // not something this build understands; skip it
		}
		out = append(out, *req)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[j].CreationTimestamp.Before(&out[i].CreationTimestamp)
	})
	return out, nil
}

// boardRequests is the read half: every standing click on one board,
// which is what the work feed and the Runs tab render as "starting" and
// "queued" rows before any sandbox exists.
func (s *Server) boardRequests(ctx context.Context, board *unstructured.Unstructured) []boardv1alpha1.Request {
	list, err := s.listRequests(ctx, board.GetNamespace(), v1.ListOptions{
		LabelSelector: boardv1alpha1.LabelBoard + "=" + board.GetName(),
	})
	if err != nil {
		// A board whose clicks cannot be read still has sandboxes, and
		// those are the more important half of the answer.
		return nil
	}
	active := list[:0]
	for _, req := range list {
		if req.Active() {
			active = append(active, req)
		}
	}
	return active
}

func requestFrom(obj *unstructured.Unstructured) (*boardv1alpha1.Request, error) {
	req := &boardv1alpha1.Request{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, req); err != nil {
		return nil, err
	}
	return req, nil
}
