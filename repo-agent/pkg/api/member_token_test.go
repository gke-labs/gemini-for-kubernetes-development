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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type unauthorizedRT struct{}

func (unauthorizedRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Body:       io.NopCloser(strings.NewReader(`{"message":"Bad credentials"}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

// withPAT gives alice's github-pat secret a manual PAT, and makes GitHub
// answer 401 for every token in rejected.
func withPAT(t *testing.T, server *Server, manual string, rejected ...string) {
	t.Helper()
	secrets := server.K8sManager.Clientset.CoreV1().Secrets("alice")
	sec, err := secrets.Get(context.Background(), "github-pat", v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sec.Data["manual_pat"] = []byte(manual)
	if _, err := secrets.Update(context.Background(), sec, v1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	prev := githubHTTPForToken
	githubHTTPForToken = func(token string) *http.Client {
		for _, r := range rejected {
			if token == r {
				return &http.Client{Transport: unauthorizedRT{}}
			}
		}
		return prev(token)
	}
	t.Cleanup(func() { githubHTTPForToken = prev })
}

func TestGetBoardWorkSkipsRejectedToken(t *testing.T) {
	server, r, _ := boardTestServer(t, nil, boardCR())
	withPAT(t, server, "ghp_stale", "ghp_stale")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/board/myboard/work", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("a stale manual PAT hid the login token: %d %s", w.Code, w.Body.String())
	}
	if token, err := server.memberToken(context.Background(), "alice"); err != nil || token != "gho_alice" {
		t.Errorf("memberToken = %q, %v; want the login token", token, err)
	}
}

func TestGetBoardWorkEveryTokenRejected(t *testing.T) {
	server, r, _ := boardTestServer(t, nil, boardCR())
	withPAT(t, server, "ghp_stale", "ghp_stale", "gho_alice")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/board/myboard/work", nil))
	if w.Code != http.StatusFailedDependency || !strings.Contains(w.Body.String(), "update it in Settings") {
		t.Fatalf("got %d %s, want 424 telling the member to update their token", w.Code, w.Body.String())
	}
}

func TestMemberTokenOrder(t *testing.T) {
	server, _, _ := boardTestServer(t, nil, boardCR())
	withPAT(t, server, "ghp_manual")
	if token, _ := server.memberToken(context.Background(), "alice"); token != "ghp_manual" {
		t.Fatalf("memberToken = %q, want the manual PAT first", token)
	}
	markTokenRejected("ghp_manual")
	if token, _ := server.memberToken(context.Background(), "alice"); token != "gho_alice" {
		t.Fatalf("memberToken = %q, want the login token once the manual PAT is rejected", token)
	}
	_, _ = server.K8sManager.Clientset.CoreV1().Secrets("alice").Create(context.Background(), &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{Name: "factory-user", Namespace: "alice"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("ghp_factory")},
	}, v1.CreateOptions{})
	markTokenRejected("gho_alice")
	if token, _ := server.memberToken(context.Background(), "alice"); token != "ghp_factory" {
		t.Fatalf("memberToken = %q, want factory-user's token last", token)
	}
}
