package models

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDraftReviewComment_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(*testing.T, *DraftReviewComment)
	}{
		{
			name: "line as int",
			yaml: `
path: file.go
line: 10
body: some comment
`,
			wantErr: false,
			check: func(t *testing.T, c *DraftReviewComment) {
				if c.Line == nil || *c.Line != 10 {
					t.Errorf("expected line 10, got %v", c.Line)
				}
			},
		},
		{
			name: "line as string",
			yaml: `
path: file.go
line: "10"
body: some comment
`,
			wantErr: false,
			check: func(t *testing.T, c *DraftReviewComment) {
				if c.Line == nil || *c.Line != 10 {
					t.Errorf("expected line 10, got %v", c.Line)
				}
			},
		},
		{
			name: "all fields as strings",
			yaml: `
path: file.go
line: "10"
position: "20"
start_line: "5"
body: some comment
`,
			wantErr: false,
			check: func(t *testing.T, c *DraftReviewComment) {
				if c.Line == nil || *c.Line != 10 {
					t.Errorf("expected line 10, got %v", c.Line)
				}
				if c.Position == nil || *c.Position != 20 {
					t.Errorf("expected position 20, got %v", c.Position)
				}
				if c.StartLine == nil || *c.StartLine != 5 {
					t.Errorf("expected start_line 5, got %v", c.StartLine)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c DraftReviewComment
			err := yaml.Unmarshal([]byte(tt.yaml), &c)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalYAML() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				tt.check(t, &c)
			}
		})
	}
}
