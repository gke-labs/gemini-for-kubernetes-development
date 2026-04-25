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

package tasks

type MockIssue struct{}

func (i MockIssue) HTMLURL() string { return "http://url" }
func (i MockIssue) Number() int     { return 123 }
func (i MockIssue) Title() string   { return "Title" }
func (i MockIssue) Body() string    { return "Body" }

type MockComment struct{}

func (c MockComment) UserLogin() string { return "User" }
func (c MockComment) Body() string      { return "Comment" }

type MockRepo struct{}

func (r MockRepo) CloneURL() string { return "http://clone" }
func (r MockRepo) Name() string     { return "repo" }
func (r MockRepo) Owner() string    { return "owner" }
func (r MockRepo) Host() string     { return "github.com" }

type MockPullRequest struct{}

func (p MockPullRequest) Number() int           { return 456 }
func (p MockPullRequest) Title() string         { return "PR Title" }
func (p MockPullRequest) TruncatedBody() string { return "PR Body" }

type MockUser struct {
	UserID string
	Email  string
	Name   string
}

type MockExtension struct {
	Source string
	Ref    string
}

type MockModel struct {
	Issue         MockIssue
	PullRequest   MockPullRequest
	Repo          MockRepo
	RepoName      string
	RepoOwner     string
	CloneURL      string
	ChoreName     string
	ChoreFile     string
	IssueComments []MockComment
	Models        []string
	User          MockUser
	PromptFile    string
	Extensions    []MockExtension
	Branch        string
	PRLabel       string
	BaseRef       string
	HeadRef       string
	SkipPR        bool
}
