package api

import (
	"testing"
)

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		name      string
		repoURL   string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "standard github url",
			repoURL:   "https://github.com/gke-labs/gemini-for-kubernetes-development",
			wantOwner: "gke-labs",
			wantRepo:  "gemini-for-kubernetes-development",
			wantErr:   false,
		},
		{
			name:      "github url with .git suffix",
			repoURL:   "https://github.com/gke-labs/gemini-for-kubernetes-development.git",
			wantOwner: "gke-labs",
			wantRepo:  "gemini-for-kubernetes-development",
			wantErr:   false,
		},
		{
			name:      "custom domain github enterprise",
			repoURL:   "https://github.example.com/owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "invalid url path",
			repoURL:   "https://github.com/onlyonepart",
			wantOwner: "",
			wantRepo:  "",
			wantErr:   true,
		},
		{
			name:      "invalid url",
			repoURL:   "://invalid-url",
			wantOwner: "",
			wantRepo:  "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := parseRepoURL(tt.repoURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseRepoURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if owner != tt.wantOwner {
				t.Errorf("parseRepoURL() owner = %v, want %v", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("parseRepoURL() repo = %v, want %v", repo, tt.wantRepo)
			}
		})
	}
}
