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

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v39/github"
)

func TestIsUserAllowed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		a     *Authenticator
		user  string
		allow bool
	}{
		{"no restrictions", &Authenticator{}, "anyone", true},
		{"listed user", &Authenticator{AllowedUsers: []string{"Alice"}}, "alice", true},
		{"unlisted user", &Authenticator{AllowedUsers: []string{"alice"}}, "bob", false},
		// Orgs alone restrict sign-in: a user must be a member.
		{"orgs only", &Authenticator{AllowedOrgs: []string{"acme"}}, "bob", false},
	} {
		if got := tc.a.IsUserAllowed(tc.user); got != tc.allow {
			t.Errorf("%s: IsUserAllowed(%q) = %v, want %v", tc.name, tc.user, got, tc.allow)
		}
	}
}

// fakeGitHub answers the two membership calls: the user's own membership
// (what read:org sees) and the public membership.
func fakeGitHub(t *testing.T, private, public map[string]bool) *github.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/memberships/orgs/{org}", func(w http.ResponseWriter, r *http.Request) {
		if !private[r.PathValue("org")] {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"state":"active","role":"member"}`))
	})
	mux.HandleFunc("GET /orgs/{org}/public_members/{user}", func(w http.ResponseWriter, r *http.Request) {
		if !public[r.PathValue("org")] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := github.NewClient(nil)
	client.BaseURL, _ = url.Parse(srv.URL + "/")
	return client
}

func TestAllowedOrg(t *testing.T) {
	ctx := context.Background()
	a := Authenticator{AllowedOrgs: []string{"acme", "GoogleCloudPlatform"}}

	if got := a.allowedOrg(ctx, fakeGitHub(t, map[string]bool{"GoogleCloudPlatform": true}, nil), "bob"); got != "GoogleCloudPlatform" {
		t.Errorf("private member: allowedOrg = %q, want GoogleCloudPlatform", got)
	}
	// The org hides a private membership from the app; a public one counts.
	if got := a.allowedOrg(ctx, fakeGitHub(t, nil, map[string]bool{"acme": true}), "bob"); got != "acme" {
		t.Errorf("public member: allowedOrg = %q, want acme", got)
	}
	if got := a.allowedOrg(ctx, fakeGitHub(t, map[string]bool{"other": true}, nil), "bob"); got != "" {
		t.Errorf("non-member: allowedOrg = %q, want none", got)
	}
}
