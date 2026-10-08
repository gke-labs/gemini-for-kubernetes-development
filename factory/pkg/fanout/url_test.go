package fanout

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicAddress(t *testing.T) {
	for _, tc := range []struct {
		addr string
		ok   bool
	}{
		{"140.82.112.3:443", true},
		{"[2606:50c0:8000::154]:443", true},
		{"127.0.0.1:443", false},
		{"[::1]:443", false},
		{"10.0.0.5:443", false},
		{"192.168.1.1:443", false},
		{"169.254.169.254:80", false},
		{"100.64.0.1:443", false},
		{"0.0.0.0:443", false},
		{"[::ffff:10.0.0.5]:443", false},
	} {
		err := publicAddress("tcp", tc.addr, nil)
		if (err == nil) != tc.ok {
			t.Errorf("publicAddress(%s) = %v, want ok %v", tc.addr, err, tc.ok)
		}
	}
}

func TestCheckItemsURL(t *testing.T) {
	for from, ok := range map[string]bool{
		"https://raw.githubusercontent.com/o/r/main/kinds.json": true,
		"http://example.com/kinds.json":                         false,
		"file:///etc/passwd":                                    false,
		"https://":                                              false,
	} {
		if err := checkItemsURL(from); (err == nil) != ok {
			t.Errorf("checkItemsURL(%s) = %v, want ok %v", from, err, ok)
		}
	}
	if _, err := Parse("## Fan-out\n```yaml\nitems: {from: \"http://example.com/k.json\"}\n```\n\n## Task\nx\n"); err == nil || !strings.Contains(err.Error(), "only https") {
		t.Errorf("an http URL parsed: %v", err)
	}
}

// kindsJSON is the shape of a migration-diff report: one element per kind,
// with the fields that differ.
const kindsJSON = `[
  {"group": "artifactregistry.cnrm.cloud.google.com", "kind": "ArtifactRegistryRepository", "diff_fields": ["cleanup_policy_dry_run", "cleanup_policies"]},
  {"group": "compute.cnrm.cloud.google.com", "kind": "ComputeRouter", "diff_fields": ["network", "region"]}
]`

func TestSyncItemsURL(t *testing.T) {
	ctx := context.Background()
	status, contentType, served := http.StatusOK, "application/json", kindsJSON
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(served))
	}))
	defer srv.Close()

	body := "## Fan-out\n```yaml\ngroup: 1\nitems: {from: \"" + srv.URL + "/diffs.json\", name: \"{{.kind}}\"}\n```\n\n" +
		"## Task\nFix the migration diffs of {{.item.kind}} ({{.item.group}}): " +
		"{{range $i, $f := .item.diff_fields}}{{if $i}}, {{end}}`{{$f}}`{{end}}. Keep journal_{{.item.kind}}.md.\n"
	opts := SyncOptions{Issue: parent, TriggerLabel: "overseer", BotLogin: "bot", HTTPClient: srv.Client()}

	gh := newFake(body)
	if _, err := Sync(ctx, gh, opts); err != nil {
		t.Fatal(err)
	}
	want := "Fix the migration diffs of ArtifactRegistryRepository (artifactregistry.cnrm.cloud.google.com): `cleanup_policy_dry_run`, `cleanup_policies`. Keep journal_ArtifactRegistryRepository.md."
	if gh.next != parent+3 || !strings.HasPrefix(gh.issues[parent+1].Body, want) || !strings.HasPrefix(gh.issues[parent+2].Body, "Fix the migration diffs of ComputeRouter") {
		t.Fatalf("got %d children, the first:\n%s", gh.next-parent-1, gh.issues[parent+1].Body)
	}
	if progress := gh.comments[parent][0].GetBody(); !strings.Contains(progress, "Items from "+srv.URL+"/diffs.json (sha256 ") {
		t.Errorf("the progress comment does not say where the items came from:\n%s", progress)
	}

	// The URL's fault is the spec's error; the server's is the pass's.
	for _, tc := range []struct {
		name        string
		status      int
		contentType string
		specError   bool
	}{
		{"not found", http.StatusNotFound, "text/plain", true},
		{"a web page", http.StatusOK, "text/html; charset=utf-8", true},
		{"unavailable", http.StatusServiceUnavailable, "text/plain", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, contentType = tc.status, tc.contentType
			res, err := Sync(ctx, newFake(body), opts)
			if (res.SpecError != nil) != tc.specError || (err != nil) == tc.specError {
				t.Errorf("SpecError %v, err %v", res.SpecError, err)
			}
		})
	}
	status, contentType = http.StatusOK, "application/json"

	// The default client reaches public addresses only: not this server.
	noClient := opts
	noClient.HTTPClient = nil
	res, err := Sync(ctx, newFake(body), noClient)
	if err != nil || res.SpecError == nil || !errors.Is(res.SpecError, errNotPublic) {
		t.Errorf("a loopback URL: SpecError %v, err %v", res.SpecError, err)
	}
}
