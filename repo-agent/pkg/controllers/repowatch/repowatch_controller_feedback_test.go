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

package repowatch

import (
	"testing"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
)

func TestReconciler_IsAuthorized(t *testing.T) {
	t.Parallel()
	g := gomega.NewWithT(t)

	r := &Reconciler{}

	issueAuthor := "issue-author"
	prAuthor := "pr-author"
	maintainer := "maintainer"
	sideActor := "side-actor"

	repoWatch := &reviewv1alpha1.RepoWatch{
		Spec: reviewv1alpha1.RepoWatchSpec{
			Maintainers: []string{maintainer},
		},
	}

	issue := &github.Issue{
		User: &github.User{Login: github.String(issueAuthor)},
	}
	pr := &github.PullRequest{
		User: &github.User{Login: github.String(prAuthor)},
	}

	g.Expect(r.isAuthorized(issueAuthor, "NONE", repoWatch, issue, pr)).To(gomega.BeTrue(), "Issue author should be authorized")
	g.Expect(r.isAuthorized(prAuthor, "NONE", repoWatch, issue, pr)).To(gomega.BeTrue(), "PR author should be authorized")
	g.Expect(r.isAuthorized(maintainer, "NONE", repoWatch, issue, pr)).To(gomega.BeTrue(), "Maintainer should be authorized")
	g.Expect(r.isAuthorized(sideActor, "NONE", repoWatch, issue, pr)).To(gomega.BeFalse(), "Side actor should NOT be authorized")

	// Test case-insensitivity
	g.Expect(r.isAuthorized("ISSUE-AUTHOR", "NONE", repoWatch, issue, pr)).To(gomega.BeTrue(), "Issue author (uppercase) should be authorized")
	g.Expect(r.isAuthorized("Maintainer", "NONE", repoWatch, issue, pr)).To(gomega.BeTrue(), "Maintainer (mixed case) should be authorized")

	// Test association authorization
	g.Expect(r.isAuthorized(sideActor, "MEMBER", repoWatch, issue, pr)).To(gomega.BeTrue(), "Repo member should be authorized")
	g.Expect(r.isAuthorized(sideActor, "OWNER", repoWatch, issue, pr)).To(gomega.BeTrue(), "Repo owner should be authorized")
	g.Expect(r.isAuthorized(sideActor, "COLLABORATOR", repoWatch, issue, pr)).To(gomega.BeTrue(), "Repo collaborator should be authorized")

	// Test with no maintainers
	repoWatchNoMaintainers := &reviewv1alpha1.RepoWatch{
		Spec: reviewv1alpha1.RepoWatchSpec{
			Maintainers: []string{},
		},
	}
	g.Expect(r.isAuthorized(issueAuthor, "NONE", repoWatchNoMaintainers, issue, pr)).To(gomega.BeTrue(), "Issue author should be authorized even with no maintainers")
	g.Expect(r.isAuthorized(prAuthor, "NONE", repoWatchNoMaintainers, issue, pr)).To(gomega.BeTrue(), "PR author should be authorized even with no maintainers")
	g.Expect(r.isAuthorized(sideActor, "NONE", repoWatchNoMaintainers, issue, pr)).To(gomega.BeFalse(), "Side actor should NOT be authorized when no maintainers are specified")

	// Test with empty author
	g.Expect(r.isAuthorized("", "MEMBER", repoWatch, issue, pr)).To(gomega.BeFalse(), "Empty author should NOT be authorized even if association is high")
}
