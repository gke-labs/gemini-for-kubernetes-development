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

package controllers

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	overseerv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/api/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/overseer"
)

// OverseerReconciler reconciles an Overseer object
type OverseerReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	RepoSandboxImage string
	ConfigDirImage   string
}

//+kubebuilder:rbac:groups=overseer.gemini.google.com,resources=overseers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=overseer.gemini.google.com,resources=overseers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=overseer.gemini.google.com,resources=overseers/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=custom.agents.x-k8s.io,resources=sandboxtasks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=custom.agents.x-k8s.io,resources=sandboxtasks/status,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=configdir.gke.io,resources=configdirs;configfiles,verbs=get;list;watch
//+kubebuilder:rbac:groups=review.gemini.google.com,resources=repowatches,verbs=get;list;watch
//+kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

const overseerFinalizer = "overseer.gemini.google.com/finalizer"

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *OverseerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var overseerObj overseerv1alpha1.Overseer
	if err := r.Get(ctx, req.NamespacedName, &overseerObj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !overseerObj.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &overseerObj)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&overseerObj, overseerFinalizer) {
		controllerutil.AddFinalizer(&overseerObj, overseerFinalizer)
		if err := r.Update(ctx, &overseerObj); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 1. Ensure Namespace exists
	nsName := fmt.Sprintf("overseer-%s", overseerObj.Name)
	if len(nsName) > 63 {
		nsName = nsName[:63]
	}
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: nsName,
		},
	}
	if err := r.Get(ctx, types.NamespacedName{Name: nsName}, ns); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating namespace for overseer", "namespace", nsName)
			if err := r.Create(ctx, ns); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			return ctrl.Result{}, err
		}
	}

	// 2. Ensure ServiceAccount and RBAC for the overseer pod
	if err := r.ensureOverseerRBAC(ctx, &overseerObj, nsName); err != nil {
		return ctrl.Result{}, err
	}

	// 3. Ensure NetworkPolicy for the sandboxes
	if err := r.ensureNetworkPolicy(ctx, nsName); err != nil {
		return ctrl.Result{}, err
	}

	// 4. Ensure secrets are present in the target namespace
	if err := r.ensureSecrets(ctx, &overseerObj, nsName); err != nil {
		return ctrl.Result{}, err
	}

	if overseerObj.Status.OverseerStatus == "Error" {
		if err := r.Status().Update(ctx, &overseerObj); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// Wait for ConfigDir to exist if referenced
	if overseerObj.Spec.ConfigdirRef != "" {
		configDir := &unstructured.Unstructured{}
		configDir.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "configdir.gke.io",
			Version: "v1alpha1",
			Kind:    "ConfigDir",
		})

		// Let's look in the target namespace (nsName).
		err := r.Get(ctx, types.NamespacedName{Name: overseerObj.Spec.ConfigdirRef, Namespace: nsName}, configDir)
		if err != nil {
			if errors.IsNotFound(err) {
				log.Info("ConfigDir not found, waiting for it to be created", "name", overseerObj.Spec.ConfigdirRef, "namespace", nsName)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			return ctrl.Result{}, err
		}
	}

	// 5. Reconcile Sandbox
	if err := overseer.ReconcileOverseer(ctx, r.Client, &overseerObj, r.RepoSandboxImage, r.ConfigDirImage); err != nil {
		return ctrl.Result{}, err
	}

	// 6. Update status
	if err := r.Status().Update(ctx, &overseerObj); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *OverseerReconciler) reconcileDelete(ctx context.Context, o *overseerv1alpha1.Overseer) (ctrl.Result, error) {
	nsName := fmt.Sprintf("overseer-%s", o.Name)
	if len(nsName) > 63 {
		nsName = nsName[:63]
	}
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: nsName,
		},
	}
	if err := r.Delete(ctx, ns); err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(o, overseerFinalizer)
	return ctrl.Result{}, r.Update(ctx, o)
}

func (r *OverseerReconciler) ensureNetworkPolicy(ctx context.Context, namespace string) error {
	log := log.FromContext(ctx)

	systemNamespace := os.Getenv("REPO_AGENT_SYSTEM_NAMESPACE")
	if systemNamespace == "" {
		systemNamespace = "repo-agent-system"
	}

	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sandbox-egress",
			Namespace: namespace,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "sandbox.gemini.google.com/type",
						Operator: metav1.LabelSelectorOpExists,
					},
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					// Allow DNS
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "kube-system",
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolUDP; return &p }(),
							Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
						},
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolTCP; return &p }(),
							Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
						},
					},
				},
				{
					// Allow Local Registry and other infrastructure in repo-agent-system
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": systemNamespace,
								},
							},
						},
					},
				},
				{
					// Allow Public Internet (Blocks all other private IPs)
					To: []networkingv1.NetworkPolicyPeer{
						{
							IPBlock: &networkingv1.IPBlock{
								CIDR: "0.0.0.0/0",
								Except: []string{
									"10.0.0.0/8",
									"172.16.0.0/12",
									"192.168.0.0/16",
									"169.254.0.0/16",
								},
							},
						},
					},
				},
			},
		},
	}

	existingNP := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: "sandbox-egress", Namespace: namespace}, existingNP)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating NetworkPolicy for sandboxes", "namespace", namespace)
			return r.Create(ctx, np)
		}
		return err
	}

	// Update if needed
	if !apiequality.Semantic.DeepEqual(existingNP.Spec, np.Spec) {
		log.Info("Updating NetworkPolicy", "namespace", namespace)
		np.ResourceVersion = existingNP.ResourceVersion
		return r.Update(ctx, np)
	}
	return nil
}

func (r *OverseerReconciler) ensureOverseerRBAC(ctx context.Context, o *overseerv1alpha1.Overseer, namespace string) error {
	log := log.FromContext(ctx)

	// ServiceAccount
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "overseer",
			Namespace: namespace,
		},
	}
	if err := r.Get(ctx, types.NamespacedName{Name: "overseer", Namespace: namespace}, sa); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating ServiceAccount for overseer pod", "namespace", namespace)
			if err := r.Create(ctx, sa); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// RoleBinding to the cluster role "overseer"
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "overseer-binding",
			Namespace: namespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "overseer",
				Namespace: namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     "overseer",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}
	if err := r.Get(ctx, types.NamespacedName{Name: "overseer-binding", Namespace: namespace}, rb); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating RoleBinding for overseer pod", "namespace", namespace)
			if err := r.Create(ctx, rb); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// Add to ClusterRoleBinding "overseer-binding"
	crb := &rbacv1.ClusterRoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Name: "overseer-binding"}, crb); err == nil {
		found := false
		for _, s := range crb.Subjects {
			if s.Kind == "ServiceAccount" && s.Name == "overseer" && s.Namespace == namespace {
				found = true
				break
			}
		}
		if !found {
			log.Info("Adding ServiceAccount to overseer-binding ClusterRoleBinding", "namespace", namespace)
			crb.Subjects = append(crb.Subjects, rbacv1.Subject{
				Kind:      "ServiceAccount",
				Name:      "overseer",
				Namespace: namespace,
			})
			if err := r.Update(ctx, crb); err != nil {
				return err
			}
		}
	} else if !errors.IsNotFound(err) {
		return err
	}

	// --- Overseer Sandbox ---
	saSandbox := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "overseer-sandbox",
			Namespace: namespace,
		},
	}
	if err := r.Get(ctx, types.NamespacedName{Name: "overseer-sandbox", Namespace: namespace}, saSandbox); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating ServiceAccount for overseer sandbox", "namespace", namespace)
			if err := r.Create(ctx, saSandbox); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// RoleBinding to the cluster role "overseer-sandbox"
	rbSandbox := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "overseer-sandbox-binding",
			Namespace: namespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "overseer-sandbox",
				Namespace: namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     "overseer-sandbox",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}
	if err := r.Get(ctx, types.NamespacedName{Name: "overseer-sandbox-binding", Namespace: namespace}, rbSandbox); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating RoleBinding for overseer sandbox", "namespace", namespace)
			if err := r.Create(ctx, rbSandbox); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// Add to ClusterRoleBinding "overseer-sandbox"
	crbSandbox := &rbacv1.ClusterRoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Name: "overseer-sandbox"}, crbSandbox); err == nil {
		found := false
		for _, s := range crbSandbox.Subjects {
			if s.Kind == "ServiceAccount" && s.Name == "overseer-sandbox" && s.Namespace == namespace {
				found = true
				break
			}
		}
		if !found {
			log.Info("Adding ServiceAccount to overseer-sandbox ClusterRoleBinding", "namespace", namespace)
			crbSandbox.Subjects = append(crbSandbox.Subjects, rbacv1.Subject{
				Kind:      "ServiceAccount",
				Name:      "overseer-sandbox",
				Namespace: namespace,
			})
			if err := r.Update(ctx, crbSandbox); err != nil {
				return err
			}
		}
	} else if !errors.IsNotFound(err) {
		return err
	}

	return nil
}

func (r *OverseerReconciler) ensureSecrets(ctx context.Context, o *overseerv1alpha1.Overseer, targetNamespace string) error {
	systemNamespace := os.Getenv("REPO_AGENT_SYSTEM_NAMESPACE")
	if systemNamespace == "" {
		systemNamespace = "repo-agent-system"
	}

	secretsToCopy := []string{o.Spec.RobotAccount, o.Spec.GeminiAPIKeySecretName, "tokenscript"}
	for _, name := range secretsToCopy {
		if name == "" {
			continue
		}
		// check if secret exists in targetNamespace
		s := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: targetNamespace}, s)
		if err == nil {
			// Found it.
			continue
		}
		if !errors.IsNotFound(err) {
			return err
		}
		// Not found, try copying from fallback namespaces
		if err := r.copySecret(ctx, name, []string{o.Namespace, "overseer-system", systemNamespace}, targetNamespace); err != nil {
			if errors.IsNotFound(err) {
				if name == "tokenscript" {
					continue // tokenscript is optional
				}
				o.Status.OverseerStatus = "Error"
				o.Status.Message = fmt.Sprintf("Secret %s not found in %s, overseer-system, or %s", name, targetNamespace, systemNamespace)
				return nil // Don't return error to stop reconcile but update status
			}
			return err
		}
	}
	return nil
}

func (r *OverseerReconciler) copySecret(ctx context.Context, name string, fromNamespaces []string, toNamespace string) error {
	log := log.FromContext(ctx)

	var sourceSecret *corev1.Secret
	var lastErr error

	for _, fromNs := range fromNamespaces {
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: fromNs}, secret)
		if err == nil {
			sourceSecret = secret
			break
		}
		if !errors.IsNotFound(err) {
			return err
		}
		lastErr = err
	}

	if sourceSecret == nil {
		if lastErr != nil {
			return lastErr
		}
		return fmt.Errorf("secret not found in any of the provided namespaces")
	}

	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: toNamespace,
		},
		Data: sourceSecret.Data,
		Type: sourceSecret.Type,
	}

	existingSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: toNamespace}, existingSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Copying secret", "name", name, "from", sourceSecret.Namespace, "to", toNamespace)
			return r.Create(ctx, targetSecret)
		}
		return err
	}

	// Update if data changed
	targetSecret.ResourceVersion = existingSecret.ResourceVersion
	return r.Update(ctx, targetSecret)
}

// SetupWithManager sets up the controller with the Manager.
func (r *OverseerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&overseerv1alpha1.Overseer{}).
		Complete(r)
}
