package commands

import "testing"

// owner and repo are taken from a user-supplied URL and end up in a
// filesystem path and a clone URL. Anything that could change the
// meaning of either has to be refused up front.
func TestValidRepoPart(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"kubernetes", true},
		{"repo-agent", true},
		{"repo_agent", true},
		{"repo.v2", true},
		{"Repo123", true},
		{"", false},
		{".", false},
		{"..", false},
		{"repo;rm -rf /", false},
		{"repo name", false},
		{"repo$(id)", false},
		{"repo`id`", false},
		{"repo/../etc", false},
		{"repo\nname", false},
		{"repo'quote", false},
		{`repo"quote`, false},
		{"repo&&touch", false},
		{"repo|tee", false},
		{"repo\x00null", false},
	}
	for _, tc := range cases {
		if got := validRepoPart(tc.in); got != tc.want {
			t.Errorf("validRepoPart(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Both halves of the URL reach a shell — as a path under /workspaces
// and as part of the clone URL the notes are pushed to — so the parse
// is also the gate.
func TestParseGitHubRepoURL(t *testing.T) {
	cases := []struct {
		in          string
		owner, repo string
		wantErr     bool
	}{
		{in: "https://github.com/gke-labs/repo-agent", owner: "gke-labs", repo: "repo-agent"},
		{in: "https://github.com/gke-labs/repo-agent.git", owner: "gke-labs", repo: "repo-agent"},
		{in: "https://github.com/gke-labs/repo-agent/", owner: "gke-labs", repo: "repo-agent"},
		{in: "https://github.com/gke-labs", wantErr: true},
		{in: "", wantErr: true},
		// A path that walks out of the checkout, and a name that would
		// end the shell word it lands in.
		{in: "https://github.com/../../etc/repo", wantErr: true},
		{in: "https://github.com/owner/repo;id", wantErr: true},
		{in: "https://github.com/owner/$(id)", wantErr: true},
	}
	for _, tc := range cases {
		owner, repo, err := parseGitHubRepoURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseGitHubRepoURL(%q) = %q/%q, want an error", tc.in, owner, repo)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseGitHubRepoURL(%q): %v", tc.in, err)
			continue
		}
		if owner != tc.owner || repo != tc.repo {
			t.Errorf("parseGitHubRepoURL(%q) = %q/%q, want %q/%q", tc.in, owner, repo, tc.owner, tc.repo)
		}
	}
}
