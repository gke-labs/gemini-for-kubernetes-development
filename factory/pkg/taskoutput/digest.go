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

package taskoutput

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
)

// Digest is a Digest document's spec: weekly retrospective findings,
// scoreboard and recommendations, posted as an issue.
type Digest struct {
	Title    string   `yaml:"title,omitempty" json:"title,omitempty"`
	Body     string   `yaml:"body,omitempty" json:"body,omitempty"`
	Markdown string   `yaml:"markdown,omitempty" json:"markdown,omitempty"`
	Labels   []string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// DigestSpec decodes a Digest document's spec.
func (d *Document) DigestSpec() (*Digest, error) {
	if d.Kind != "Digest" {
		return nil, fmt.Errorf("%s task output is not a Digest", d.Kind)
	}
	var s Digest
	if err := d.Spec.Decode(&s); err != nil {
		return nil, fmt.Errorf("Digest spec: %w", err)
	}
	if strings.TrimSpace(s.Title) == "" {
		return nil, fmt.Errorf("Digest spec has no title")
	}
	if strings.TrimSpace(s.Body) == "" && strings.TrimSpace(s.Markdown) == "" {
		return nil, fmt.Errorf("Digest spec has no body")
	}
	if s.Body == "" {
		s.Body = s.Markdown
	}
	return &s, nil
}

func parseDigest(raw string) (any, error) {
	clean := CleanAgentYAML(raw, "digest:")
	var out struct {
		Digest *Digest `yaml:"digest"`
	}
	if err := yaml.Unmarshal([]byte(clean), &out); err == nil && out.Digest != nil {
		d := out.Digest
		d.Title = strings.TrimSpace(d.Title)
		d.Body = strings.TrimSpace(d.Body)
		d.Markdown = strings.TrimSpace(d.Markdown)
		if d.Body == "" && d.Markdown != "" {
			d.Body = d.Markdown
		}
		if d.Title == "" && strings.HasPrefix(d.Body, "# ") {
			d.Title, d.Body = extractMarkdownTitle(d.Body)
		}
		if d.Title == "" {
			return nil, fmt.Errorf("the digest has no title")
		}
		if d.Body == "" {
			return nil, fmt.Errorf("the digest has no body")
		}
		return d, nil
	}

	var d Digest
	if err := yaml.Unmarshal([]byte(clean), &d); err == nil && (d.Title != "" || d.Body != "" || d.Markdown != "") {
		d.Title = strings.TrimSpace(d.Title)
		d.Body = strings.TrimSpace(d.Body)
		d.Markdown = strings.TrimSpace(d.Markdown)
		if d.Body == "" && d.Markdown != "" {
			d.Body = d.Markdown
		}
		if d.Title == "" && strings.HasPrefix(d.Body, "# ") {
			d.Title, d.Body = extractMarkdownTitle(d.Body)
		}
		if d.Title != "" && d.Body != "" {
			return &d, nil
		}
	}

	md := CleanAgentMarkdown(raw)
	if md == "" {
		return nil, fmt.Errorf("the digest is empty")
	}
	title, body := extractMarkdownTitle(md)
	if title == "" {
		return nil, fmt.Errorf("the digest markdown has no title heading")
	}
	if body == "" {
		return nil, fmt.Errorf("the digest has no body")
	}
	return &Digest{Title: title, Body: body, Markdown: body}, nil
}

func extractMarkdownTitle(md string) (string, string) {
	lines := strings.Split(strings.TrimSpace(md), "\n")
	if len(lines) == 0 {
		return "", ""
	}
	first := strings.TrimSpace(lines[0])
	if strings.HasPrefix(first, "# ") {
		title := strings.TrimSpace(strings.TrimPrefix(first, "# "))
		body := strings.TrimSpace(strings.Join(lines[1:], "\n"))
		return title, body
	}
	return "", strings.TrimSpace(md)
}

func digestRepoTarget(doc *Document) (owner, repo string, err error) {
	u, err := url.Parse(doc.Target.URL)
	if err != nil {
		return "", "", fmt.Errorf("target %q: %w", doc.Target.URL, err)
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Host != "github.com" || len(p) < 2 || p[0] == "" || p[1] == "" {
		return "", "", fmt.Errorf("%s targets a repository (https://github.com/owner/repo), not %s", doc.Kind, doc.Target.URL)
	}
	return p[0], strings.TrimSuffix(p[1], ".git"), nil
}

// applyOpenIssue opens an issue on the target repository with the Digest's title
// and body, labelled with <trigger>/retro.
func applyOpenIssue(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	d, err := doc.DigestSpec()
	if err != nil {
		return err
	}
	owner, repo, err := digestRepoTarget(doc)
	if err != nil {
		return err
	}

	trigger := triggerLabel(ctx)
	retroLabel := trigger + "/retro"

	var labels []string
	labels = append(labels, retroLabel)
	for _, l := range d.Labels {
		if !slices.Contains(labels, l) {
			labels = append(labels, l)
		}
	}

	// If target URL was already pointed to an issue, avoid opening again.
	if strings.Contains(doc.Target.URL, "/issues/") {
		fmt.Fprintf(out, "The Digest of task %s is already on %s; not opening again\n", doc.Source.Task, doc.Target.URL)
		return nil
	}

	mark := marker(doc)
	if mark != "" {
		opts := &githubv39.IssueListByRepoOptions{
			Labels:      []string{retroLabel},
			State:       "all",
			ListOptions: githubv39.ListOptions{PerPage: 50},
		}
		existingIssues, _, err := gh.Issues.ListByRepo(ctx, owner, repo, opts)
		if err == nil {
			for _, iss := range existingIssues {
				if strings.Contains(iss.GetBody(), strings.TrimSpace(mark)) {
					doc.Target.URL = iss.GetHTMLURL()
					fmt.Fprintf(out, "The Digest of task %s is already on %s; not opening again\n", doc.Source.Task, doc.Target.URL)
					return nil
				}
			}
		}
	}

	fmt.Fprintf(out, "%s an issue on %s/%s labelled %s: %q\n", doing(dryRun, "Opening", "open"), owner, repo, strings.Join(labels, ", "), d.Title)
	if dryRun {
		return nil
	}

	body := strings.TrimSpace(d.Body)
	if mark != "" {
		body += mark
	}

	req := &githubv39.IssueRequest{
		Title:  githubv39.String(d.Title),
		Body:   githubv39.String(body),
		Labels: &labels,
	}

	issue, _, err := gh.Issues.Create(ctx, owner, repo, req)
	if err != nil {
		return fmt.Errorf("opening the issue: %w", err)
	}

	doc.Target.URL = issue.GetHTMLURL()
	fmt.Fprintf(out, "Opened %s\n", doc.Target.URL)
	return nil
}
