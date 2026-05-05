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

package syncer

import (
	"context"
	"fmt"
	"sync"

	syncerv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/syncer/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/gcs"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/decls"
	"github.com/google/cel-go/common/types"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler reconciles a Syncer object
type Reconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Manager   ctrl.Manager
	GCSClient gcs.Uploader

	watchedGVKs map[schema.GroupVersionKind]bool
	mu          sync.Mutex
}

//+kubebuilder:rbac:groups=syncer.gemini.google.com,resources=syncers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=syncer.gemini.google.com,resources=syncers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=syncer.gemini.google.com,resources=syncers/finalizers,verbs=update
//+kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Fetch the Syncer instance
	var syncer syncerv1alpha1.Syncer
	if err := r.Get(ctx, req.NamespacedName, &syncer); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, rule := range syncer.Spec.Rules {
		gvk := schema.GroupVersionKind{
			Group:   rule.Group,
			Version: rule.Version,
			Kind:    rule.Kind,
		}

		if !r.watchedGVKs[gvk] {
			if err := r.startWatcher(ctx, gvk); err != nil {
				// Log error but continue
				log.Log.Error(err, "Failed to start watcher for GVK", "gvk", gvk)
				continue
			}
			r.watchedGVKs[gvk] = true
		}
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) startWatcher(ctx context.Context, gvk schema.GroupVersionKind) error {
	log.Log.Info("Starting watcher for GVK", "gvk", gvk)

	dr := &DynamicResourceReconciler{
		Client:    r.Client,
		GVK:       gvk,
		GCSClient: r.GCSClient,
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)

	return ctrl.NewControllerManagedBy(r.Manager).
		For(u).
		Named(fmt.Sprintf("dynamic-controller-%s-%s-%s", gvk.Group, gvk.Version, gvk.Kind)).
		Complete(dr)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.watchedGVKs = make(map[schema.GroupVersionKind]bool)
	return ctrl.NewControllerManagedBy(mgr).
		For(&syncerv1alpha1.Syncer{}).
		Complete(r)
}

// DynamicResourceReconciler reconciles dynamic resources
type DynamicResourceReconciler struct {
	client.Client
	GVK       schema.GroupVersionKind
	GCSClient gcs.Uploader
}

func (r *DynamicResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the resource
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(r.GVK)
	if err := r.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Fetch all Syncers to find matching rules
	// In a real generic controller, we might index rules. For now, list all.
	var syncers syncerv1alpha1.SyncerList
	if err := r.List(ctx, &syncers); err != nil {
		return ctrl.Result{}, err
	}

	for _, syncer := range syncers.Items {
		for _, rule := range syncer.Spec.Rules {
			// Check if rule applies to this GVK
			if rule.Group != r.GVK.Group || rule.Version != r.GVK.Version || rule.Kind != r.GVK.Kind {
				continue
			}

			// Check Namespace
			if len(rule.Namespaces) > 0 {
				found := false
				for _, ns := range rule.Namespaces {
					if ns == u.GetNamespace() {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}

			// Check CEL
			if rule.MatchCEL != "" {
				matches, err := evalCEL(rule.MatchCEL, u.Object)
				if err != nil {
					log.Error(err, "CEL evaluation failed", "rule", rule.MatchCEL)
					continue
				}
				if !matches {
					continue
				}
			}

			// If we are here, it matches. Sync to GCS.
			if err := r.syncToGCS(ctx, &syncer, u); err != nil {
				log.Error(err, "Failed to sync to GCS")
			}
		}
	}

	return ctrl.Result{}, nil
}

func (r *DynamicResourceReconciler) syncToGCS(ctx context.Context, syncer *syncerv1alpha1.Syncer, u *unstructured.Unstructured) error {
	// Pattern: installation-name/resource-type/namespace/<resource>.yaml
	// resource-type could be Kind
	resourceType := r.GVK.Kind
	namespace := u.GetNamespace()
	if namespace == "" {
		namespace = "cluster-scoped"
	}
	name := u.GetName()

	path := fmt.Sprintf("%s/%s/%s/%s.yaml", syncer.Spec.InstallationName, resourceType, namespace, name)

	// Serialize to YAML
	data, err := yaml.Marshal(u.Object)
	if err != nil {
		return fmt.Errorf("failed to marshal yaml: %w", err)
	}

	// Upload
	return r.GCSClient.Upload(ctx, syncer.Spec.GCSBucketName, path, data)
}

func evalCEL(expression string, obj map[string]interface{}) (bool, error) {
	env, err := cel.NewEnv(
		cel.VariableDecls(
			decls.NewVariable("object", types.DynType),
			decls.NewVariable("self", types.DynType),
		),
	)
	if err != nil {
		return false, err
	}

	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return false, issues.Err()
	}

	prg, err := env.Program(ast)
	if err != nil {
		return false, err
	}

	out, _, err := prg.Eval(map[string]interface{}{
		"object": obj,
		"self":   obj,
	})
	if err != nil {
		return false, err
	}

	val := out.Value()
	if b, ok := val.(bool); ok {
		return b, nil
	}
	return false, fmt.Errorf("CEL expression did not return a boolean")
}
