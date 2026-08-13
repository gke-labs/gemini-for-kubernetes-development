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
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	overseerv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/api/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/overseer"
)

// OverseerReconciler reconciles an Overseer object
type OverseerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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
//+kubebuilder:rbac:groups="",resources=pods,verbs=delete
//+kubebuilder:rbac:groups="",resources=pods/portforward,verbs=create
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *OverseerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var overseerObj overseerv1alpha1.Overseer
	if err := r.Get(ctx, req.NamespacedName, &overseerObj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Reset status on every reconcile to clear previous errors
	overseerObj.Status.OverseerStatus = ""
	overseerObj.Status.Message = ""

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

	// 3. Ensure secrets are present in the target namespace
	if err := r.ensureSecrets(ctx, &overseerObj, nsName); err != nil {
		return ctrl.Result{}, err
	}

	if overseerObj.Status.OverseerStatus == "Error" {
		overseerObj.Status.ObservedGeneration = overseerObj.Generation
		if err := r.Status().Update(ctx, &overseerObj); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// 4. Reconcile Sandbox
	if err := overseer.ReconcileOverseer(ctx, r.Client, &overseerObj); err != nil {
		return ctrl.Result{}, err
	}

	// 5. Update status
	overseerObj.Status.ObservedGeneration = overseerObj.Generation
	if err := r.Status().Update(ctx, &overseerObj); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
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

	// 1. Reconcile system secrets (tokenscript, github-portal-ca) by copying them
	systemSecrets := []string{"tokenscript", "github-portal-ca"}
	for _, name := range systemSecrets {
		// check if secret exists in targetNamespace
		s := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: targetNamespace}, s)
		if err == nil {
			continue
		}
		if !errors.IsNotFound(err) {
			return err
		}
		if err := r.copySecret(ctx, name, []string{o.Namespace, "overseer-system"}, targetNamespace); err != nil {
			if errors.IsNotFound(err) {
				if name == "tokenscript" {
					continue // tokenscript is optional
				}
				o.Status.OverseerStatus = "Error"
				o.Status.Message = fmt.Sprintf("Secret %s not found in %s or overseer-system", name, targetNamespace)
				return nil
			}
			return err
		}
	}

	// 2. Resolve credentials secret and Gemini API key secret from source namespaces
	var githubLogin, githubEmail, githubToken []byte
	if o.Spec.RobotAccount != "" {
		srcRobotSecret, err := r.findSecret(ctx, o.Spec.RobotAccount, []string{o.Namespace, "overseer-system"})
		if err != nil {
			if errors.IsNotFound(err) {
				o.Status.OverseerStatus = "Error"
				o.Status.Message = fmt.Sprintf("Robot account secret %s not found in %s or overseer-system", o.Spec.RobotAccount, targetNamespace)
				return nil
			}
			return err
		}

		githubLogin = getSecretValue(srcRobotSecret, "GITHUB_LOGIN", "userid")
		githubEmail = getSecretValue(srcRobotSecret, "GITHUB_EMAIL", "email")
		githubToken = getSecretValue(srcRobotSecret, "GITHUB_TOKEN", "pat")
	}

	var geminiAPIKey []byte
	geminiSecretName := o.Spec.GeminiAPIKeySecretName
	if geminiSecretName == "" {
		geminiSecretName = "gemini-api-key"
	}
	srcGeminiSecret, err := r.findSecret(ctx, geminiSecretName, []string{o.Namespace, "overseer-system"})
	if err != nil {
		if errors.IsNotFound(err) {
			o.Status.OverseerStatus = "Error"
			o.Status.Message = fmt.Sprintf("Gemini API key secret %s not found in %s or overseer-system", geminiSecretName, targetNamespace)
			return nil
		}
		return err
	}
	geminiAPIKey = getSecretValue(srcGeminiSecret, "GEMINI_API_KEY", "gemini")

	// 3. Create or Update "factory-user" secret in target namespace
	if err := r.reconcileFactorySecret(ctx, "factory-user", targetNamespace, githubLogin, githubEmail, githubToken, geminiAPIKey); err != nil {
		return err
	}

	// 4. Create or Update user pool secrets
	for _, roleSpec := range o.Spec.Roles {
		for _, user := range roleSpec.Users {
			if user == "" {
				continue
			}

			userSecret, err := r.findSecret(ctx, fmt.Sprintf("user-%s", user), []string{o.Namespace, "overseer-system"})
			if err != nil {
				if errors.IsNotFound(err) {
					userSecret, err = r.findSecret(ctx, user, []string{o.Namespace, "overseer-system"})
				}
			}
			if err != nil {
				if errors.IsNotFound(err) {
					userSecret, err = r.findSecret(ctx, fmt.Sprintf("factory-user-%s", user), []string{o.Namespace, "overseer-system"})
				}
			}

			if err != nil {
				if errors.IsNotFound(err) {
					o.Status.OverseerStatus = "Error"
					o.Status.Message = fmt.Sprintf("Secret for user %s not found in %s or overseer-system", user, o.Namespace)
					return nil
				}
				return err
			}

			uLogin := getSecretValue(userSecret, "GITHUB_LOGIN", "userid")
			uEmail := getSecretValue(userSecret, "GITHUB_EMAIL", "email")
			uToken := getSecretValue(userSecret, "GITHUB_TOKEN", "pat")

			targetSecretName := fmt.Sprintf("user-%s", user)
			if err := r.reconcileFactorySecret(ctx, targetSecretName, targetNamespace, uLogin, uEmail, uToken, geminiAPIKey); err != nil {
				return err
			}
		}
	}

	return nil
}

func (r *OverseerReconciler) reconcileFactorySecret(ctx context.Context, name, targetNamespace string, githubLogin, githubEmail, githubToken, geminiAPIKey []byte) error {
	log := log.FromContext(ctx)

	factorySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: targetNamespace,
		},
		Data: map[string][]byte{},
	}
	if len(githubLogin) > 0 {
		factorySecret.Data["GITHUB_LOGIN"] = githubLogin
	}
	if len(githubEmail) > 0 {
		factorySecret.Data["GITHUB_EMAIL"] = githubEmail
	}
	if len(githubToken) > 0 {
		factorySecret.Data["GITHUB_TOKEN"] = githubToken
	}
	if len(geminiAPIKey) > 0 {
		factorySecret.Data["GEMINI_API_KEY"] = geminiAPIKey
	}

	existingSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: targetNamespace}, existingSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating factory secret", "name", name, "namespace", targetNamespace)
			return r.Create(ctx, factorySecret)
		}
		return err
	}

	// Update if changed
	factorySecret.ResourceVersion = existingSecret.ResourceVersion
	return r.Update(ctx, factorySecret)
}

func (r *OverseerReconciler) findSecret(ctx context.Context, name string, namespaces []string) (*corev1.Secret, error) {
	var lastErr error
	for _, ns := range namespaces {
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, secret)
		if err == nil {
			return secret, nil
		}
		if !errors.IsNotFound(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func getSecretValue(s *corev1.Secret, key string, fallbackKey string) []byte {
	if s == nil || s.Data == nil {
		return nil
	}
	if val, ok := s.Data[key]; ok && len(val) > 0 {
		return val
	}
	if val, ok := s.Data[fallbackKey]; ok && len(val) > 0 {
		return val
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
