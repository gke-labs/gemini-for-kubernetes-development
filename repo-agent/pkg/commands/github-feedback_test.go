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

package commands

import (
	"testing"

	"github.com/onsi/gomega"
)

// We need to satisfy the interfaces if they exist, or just use real github objects if preferred.
// github-feedback.go uses github.Issue and github.PullRequest wrappers from pkg/github.

func TestGithubFeedbackCommand_IsAuthorized(t *testing.T) {
	t.Parallel()
	g := gomega.NewWithT(t)

	c := &GithubFeedbackCommand{
		GithubUserLogin: "user-account",
		GithubBotLogin:  "bot-account",
		maintainers:     []string{"maintainer1"},
	}

	// Since we can't easily mock the pkg/github wrappers without more effort,
	// let's see if we can use them or if we need to adjust the test.
	// Actually, c.issue and c.pullRequest are pointers to pkg/github.Issue and pkg/github.PullRequest.

	// For this test, we can just test the parts that don't depend on issue/PR first,
	// or initialize them if possible.

	g.Expect(c.isAuthorized("user-account", "NONE")).To(gomega.BeTrue(), "User account should be authorized")
	g.Expect(c.isAuthorized("bot-account", "NONE")).To(gomega.BeTrue(), "Bot account should be authorized")
	g.Expect(c.isAuthorized("BOT-ACCOUNT", "NONE")).To(gomega.BeTrue(), "Bot account (uppercase) should be authorized")

	g.Expect(c.isAuthorized("maintainer1", "NONE")).To(gomega.BeTrue(), "Maintainer should be authorized")
	g.Expect(c.isAuthorized("MAINTAINER1", "NONE")).To(gomega.BeTrue(), "Maintainer (uppercase) should be authorized")

	g.Expect(c.isAuthorized("random-user", "OWNER")).To(gomega.BeTrue(), "Owner should be authorized")
	g.Expect(c.isAuthorized("random-user", "MEMBER")).To(gomega.BeTrue(), "Member should be authorized")
	g.Expect(c.isAuthorized("random-user", "COLLABORATOR")).To(gomega.BeTrue(), "Collaborator should be authorized")

	g.Expect(c.isAuthorized("random-user", "NONE")).To(gomega.BeFalse(), "Random user should NOT be authorized")
	g.Expect(c.isAuthorized("", "OWNER")).To(gomega.BeFalse(), "Empty author should NOT be authorized")
}
