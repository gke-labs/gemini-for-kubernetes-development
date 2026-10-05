package tasks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gemini's session cleanup, on by default, deletes a conversation that
// session/load resumed (see configureGemini); acpd's continue needs it off.
func TestConfigureGeminiKeepsSessions(t *testing.T) {
	lib, err := scriptsFS.ReadFile("lib.sh")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cmd := exec.Command("bash", "-c", string(lib)+"\nconfigureGemini")
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configureGemini: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		General struct {
			SessionRetention *struct {
				Enabled *bool `json:"enabled"`
			} `json:"sessionRetention"`
		} `json:"general"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("settings.json is not JSON: %v\n%s", err, raw)
	}
	if r := settings.General.SessionRetention; r == nil || r.Enabled == nil || *r.Enabled {
		t.Errorf("settings.json leaves gemini's session cleanup on:\n%s", raw)
	}
}
