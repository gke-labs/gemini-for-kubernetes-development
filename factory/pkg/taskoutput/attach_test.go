package taskoutput

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Refresh reads the attached files into the result: their content and the
// size and sha256 of each whole file. A file the task did not write is
// left out.
func TestRefreshReadsAttachments(t *testing.T) {
	dir := t.TempDir()
	capture := "# Evidence capture\n\n| method | outcome |\n|---|---|\n| Create | OK |\n"
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("audit-capture.md", capture)
	frame := assessDoc(t, "recipe-assess-1", &CommentOptions{Attach: []string{"audit-capture.md", "missing.md"}})
	if err := WriteFrame(dir, frame); err != nil {
		t.Fatal(err)
	}
	write("triage-output.yaml", "triage:\n  labels: [overseer/assessment/good]\n  assessment: ok\n")
	if _, err := Refresh(dir, "triage-output.yaml"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, File))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	got := docs[0].Attachments
	sum := sha256.Sum256([]byte(capture))
	if len(got) != 1 || got[0].Name != "audit-capture.md" || got[0].Content != capture || got[0].Size != len(capture) || got[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("attachments = %+v", got)
	}
}

// The comment carries each attachment verbatim in a collapsed block that
// names the file, its size and sha256; a file that is not markdown is
// fenced.
func TestCommentWithAttachments(t *testing.T) {
	f := &fakeComments{}
	var out bytes.Buffer
	doc := assessDoc(t, "recipe-assess-1", &CommentOptions{Prefix: "/overseer-ignore"})
	doc.Attachments = []Attachment{
		{Name: "audit-capture.md", Size: 25, SHA256: "abc", Content: "| a | b |\n|---|---|\n| 1 | 2 |\n"},
		{Name: "audit-query.yaml", Size: 9, SHA256: "def", Content: "project: p\n"},
	}
	if err := ApplyAction(context.Background(), f.client(t), doc, "comment", false, &out); err != nil {
		t.Fatal(err)
	}
	b := f.comments[0].GetBody()
	for _, want := range []string{
		"/overseer-ignore\n\n**Triage assessment**",
		"<summary>📎 <code>audit-capture.md</code> · 25 bytes · sha256 <code>abc</code></summary>\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n</details>",
		"<summary>📎 <code>audit-query.yaml</code> · 9 bytes · sha256 <code>def</code></summary>\n\n````\nproject: p\n````\n\n</details>",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("body lacks %q:\n%s", want, b)
		}
	}
	if strings.Index(b, "Suggested labels") > strings.Index(b, "<details>") {
		t.Errorf("attachments come before the result's own lines:\n%s", b)
	}
	if strings.Contains(b, "Cut to fit") {
		t.Errorf("nothing should be cut:\n%s", b)
	}
}

// What does not fit GitHub's limit is cut at a line end, and the block
// says how much is shown; the comment stays under the limit.
func TestCommentAttachmentCut(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	big := strings.Repeat(line, 900) // 90,000 bytes
	f := &fakeComments{}
	var out bytes.Buffer
	doc := assessDoc(t, "recipe-assess-1", nil)
	doc.Attachments = []Attachment{{Name: "audit-capture.md", Size: len(big), SHA256: "abc", Content: cutAtLine(big, MaxAttachment)}}
	if err := ApplyAction(context.Background(), f.client(t), doc, "comment", false, &out); err != nil {
		t.Fatal(err)
	}
	b := f.comments[0].GetBody()
	if n := utf8.RuneCountInString(b); n > CommentLimit {
		t.Errorf("comment is %d characters, over %d", n, CommentLimit)
	}
	if !strings.Contains(b, "_Cut to fit the comment:") || !strings.Contains(b, "of 90000 bytes shown") {
		t.Errorf("no cut note:\n%s", b[len(b)-400:])
	}
	if !strings.Contains(b, strings.Repeat("x", 99)+"\n\n_Cut") {
		t.Errorf("not cut at a line end:\n%s", b[len(b)-400:])
	}
}

func TestReadAttachmentsCapsContent(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat(strings.Repeat("y", 49)+"\n", 2000) // 100,000 bytes
	if err := os.WriteFile(filepath.Join(dir, "c.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadAttachments(dir, &CommentOptions{Attach: []string{"c.md", "../etc/passwd", ".hidden"}})
	if len(got) != 1 || got[0].Size != len(big) || len(got[0].Content) > MaxAttachment || !strings.HasSuffix(got[0].Content, "\n") {
		t.Fatalf("got %d attachments, size %d, content %d", len(got), got[0].Size, len(got[0].Content))
	}
}
