package repoboard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GCP Secret Manager references: members store a resource name
// (projects/P/secrets/S/versions/latest) instead of pasting a value,
// and grant THIS controller's Workload Identity principal
// secretmanager.secretAccessor on it. The controller is the only
// component that ever talks to Secret Manager — one principal to
// grant, one to revoke.
const (
	RefKeyPAT       = "pat-ref"
	RefKeyGemini    = "gemini-ref"
	RefKeyAnthropic = "claude-ref"
)

// gsmCache holds resolved values briefly so reconciles don't hammer
// Secret Manager (and rotation via a new version lands within ~10m).
var gsmCache = struct {
	sync.Mutex
	entries map[string]gsmEntry
}{entries: map[string]gsmEntry{}}

type gsmEntry struct {
	value   string
	expires time.Time
}

func metadataAccessToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata token: HTTP %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// resolveSecretRef fetches one Secret Manager version's payload using
// the controller's Workload Identity. ref is the resource name, e.g.
// projects/my-proj/secrets/gemini-key/versions/latest.
func resolveSecretRef(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("empty secret reference")
	}
	if !strings.HasPrefix(ref, "projects/") || strings.Contains(ref, "..") {
		return "", fmt.Errorf("secret reference must be projects/<p>/secrets/<s>/versions/<v>")
	}
	if !strings.Contains(ref, "/versions/") {
		ref += "/versions/latest"
	}

	gsmCache.Lock()
	if e, ok := gsmCache.entries[ref]; ok && time.Now().Before(e.expires) {
		gsmCache.Unlock()
		return e.value, nil
	}
	gsmCache.Unlock()

	token, err := metadataAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("controller has no GCP identity (Workload Identity required): %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://secretmanager.googleapis.com/v1/"+ref+":access", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("secretmanager %s: HTTP %d (is the controller principal granted secretAccessor?)", ref, resp.StatusCode)
	}
	var out struct {
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(out.Payload.Data)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))

	gsmCache.Lock()
	gsmCache.entries[ref] = gsmEntry{value: value, expires: time.Now().Add(10 * time.Minute)}
	gsmCache.Unlock()
	return value, nil
}
