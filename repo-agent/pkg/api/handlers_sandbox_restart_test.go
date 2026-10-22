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

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
)

// Restart deletes the sandbox's pod, for the controller to make again on
// the same disk, and stamps the wake time; a paused sandbox has no pod to
// restart and says so.
func TestSandboxLifecycleRestart(t *testing.T) {
	gvrSandbox := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvrSandbox: "SandboxList"})
	for _, sb := range []struct {
		name   string
		paused bool
	}{{"rsch-repo-1", false}, {"rsch-repo-2", true}} {
		obj := overseerSandbox(sb.name, sb.paused)
		obj.SetNamespace("alice")
		if _, err := dyn.Resource(gvrSandbox).Namespace("alice").Create(context.Background(), obj, v1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	clientset := kubernetesfake.NewClientset(researchPod("alice", "rsch-repo-1", "10.1.2.3", corev1.PodSucceeded))
	manager := &k8s.Manager{Client: dyn, Clientset: clientset}
	server := &Server{K8sManager: manager, Auth: &auth.Authenticator{K8sManager: manager}}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(auth.UserKey, "alice"); c.Set(auth.NamespaceKey, "alice"); c.Next() })
	r.POST("/sandbox-card/:name/lifecycle", server.sandboxLifecycle)
	post := func(name, action string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/sandbox-card/"+name+"/lifecycle", strings.NewReader(`{"action":"`+action+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	if w := post("rsch-repo-1", "restart"); w.Code != http.StatusOK {
		t.Fatalf("restart: %d %s", w.Code, w.Body.String())
	}
	if _, err := clientset.CoreV1().Pods("alice").Get(context.Background(), "rsch-repo-1", v1.GetOptions{}); err == nil {
		t.Error("the pod is still there after a restart")
	}
	sb, _ := dyn.Resource(gvrSandbox).Namespace("alice").Get(context.Background(), "rsch-repo-1", v1.GetOptions{})
	if sb.GetAnnotations()["sandbox.gemini.google.com/unpaused-at"] == "" {
		t.Errorf("no wake stamp: %v", sb.GetAnnotations())
	}
	// Again, with the pod already gone (the controller has not made it
	// yet): still fine.
	if w := post("rsch-repo-1", "restart"); w.Code != http.StatusOK {
		t.Errorf("restart with no pod: %d %s", w.Code, w.Body.String())
	}
	if w := post("rsch-repo-2", "restart"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "paused") {
		t.Errorf("restart of a paused sandbox: %d %s, want 409", w.Code, w.Body.String())
	}
	if w := post("rsch-repo-9", "restart"); w.Code == http.StatusOK {
		t.Errorf("restart of a missing sandbox: %d, want an error", w.Code)
	}
}
