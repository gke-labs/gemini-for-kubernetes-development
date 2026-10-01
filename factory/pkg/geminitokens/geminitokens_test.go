package geminitokens

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestIsFatalQuotaError(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "transient 429 with backoff retry",
			input:    `Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"RESOURCE_EXHAUSTED"}}`,
			expected: false,
		},
		{
			name:     "transient 429 with standard googleapis boilerplate and backoff retry",
			input:    `Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"You exceeded your current quota, please check your plan and billing details.","code":429,"status":"Too Many Requests"}}`,
			expected: false,
		},
		{
			name:     "unretried quota exhaustion message",
			input:    `You exceeded your current quota, please check your plan and billing details.`,
			expected: true,
		},
		{
			name:     "max retries exceeded after transient retries",
			input:    `Max retries exceeded for status: 429`,
			expected: true,
		},
		{
			name:     "unhandled 429 without backoff",
			input:    `Error: status: 429 Too Many Requests`,
			expected: true,
		},
		{
			name:     "fatal RPD daily quota during retry attempt",
			input:    `Attempt 3 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"Quota exceeded for quota metric 'Generate requests per day'","code":429,"status":"Too Many Requests"}}`,
			expected: true,
		},
		{
			name:     "normal output",
			input:    `Generated code successfully`,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsFatalQuotaError([]byte(tc.input))
			if got != tc.expected {
				t.Errorf("IsFatalQuotaError(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestIsTransientRateLimit(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "transient retry log",
			input:    `Attempt 1 failed with status 429. Retrying with backoff...`,
			expected: true,
		},
		{
			name:     "transient retry log with standard googleapis boilerplate",
			input:    `Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"You exceeded your current quota, please check your plan and billing details.","code":429,"status":"Too Many Requests"}}`,
			expected: true,
		},
		{
			name:     "unretried quota exhaustion log",
			input:    `You exceeded your current quota, please check your plan and billing details.`,
			expected: false,
		},
		{
			name:     "fatal RPD daily quota during retry attempt",
			input:    `Attempt 3 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"Quota exceeded for quota metric 'Generate requests per day'","code":429,"status":"Too Many Requests"}}`,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsTransientRateLimit([]byte(tc.input))
			if got != tc.expected {
				t.Errorf("IsTransientRateLimit(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestIsSuspendedKeyError(t *testing.T) {
	suspendedPayload := `{"error":{"type":"Error","message":"{\"error\":{\"message\":\"{\\n  \\\"error\\\": {\\n    \\\"code\\\": 403,\\n    \\\"message\\\": \\\"Permission denied: Consumer 'api_key:AIzaSyDUMMY_SUSPENDED_TOKEN_FOR_TESTING_12345' has been suspended.\\\",\\n    \\\"status\\\": \\\"PERMISSION_DENIED\\\",\\n    \\\"details\\\": [\\n      {\\n        \\\"@type\\\": \\\"type.googleapis.com/google.rpc.ErrorInfo\\\",\\n        \\\"reason\\\": \\\"CONSUMER_SUSPENDED\\\",\\n        \\\"domain\\\": \\\"googleapis.com\\\",\\n        \\\"metadata\\\": {\\n          \\\"consumer\\\": \\\"projects/36948037873\\\",\\n          \\\"containerInfo\\\": \\\"api_key:AIzaSyDUMMY_SUSPENDED_TOKEN_FOR_TESTING_12345\\\",\\n          \\\"service\\\": \\\"generativelanguage.googleapis.com\\\"\\n        }\\n      }\\n    ]\\n  }\\n}\\n\",\"code\":403,\"status\":\"Forbidden\"}}","code":403}}`

	if !IsSuspendedKeyError([]byte(suspendedPayload)) {
		t.Errorf("IsSuspendedKeyError expected true for CONSUMER_SUSPENDED payload, got false")
	}

	if !IsFatalQuotaError([]byte(suspendedPayload)) {
		t.Errorf("IsFatalQuotaError expected true for CONSUMER_SUSPENDED payload, got false")
	}
}

func TestExtractAPIKeyFromError(t *testing.T) {
	suspendedPayload := `Permission denied: Consumer 'api_key:AIzaSyDUMMY_SUSPENDED_TOKEN_FOR_TESTING_12345' has been suspended.`
	extracted := ExtractAPIKeyFromError([]byte(suspendedPayload))
	expected := "AIzaSyDUMMY_SUSPENDED_TOKEN_FOR_TESTING_12345"

	if extracted != expected {
		t.Errorf("ExtractAPIKeyFromError got %q, want %q", extracted, expected)
	}
}

func TestAddSuspendedKey(t *testing.T) {
	_ = os.Remove(getQuotaExceededFilePath())
	_ = os.Remove(getSuspendedFilePath())
	defer func() {
		_ = os.Remove(getQuotaExceededFilePath())
		_ = os.Remove(getSuspendedFilePath())
	}()

	testKey := "AIzaSyDUMMY_FULL_SUSPENDED_KEY_99999"
	if err := AddSuspendedKey(testKey); err != nil {
		t.Fatalf("AddSuspendedKey failed: %v", err)
	}

	if !IsKeySuspended(testKey) {
		t.Errorf("IsKeySuspended(%q) expected true, got false", testKey)
	}

	status, err := GetTokensStatus()
	if err != nil {
		t.Fatalf("GetTokensStatus failed: %v", err)
	}

	expectedObscured := testKey
	if len(expectedObscured) > 8 {
		expectedObscured = expectedObscured[:8] + "..."
	}

	found := false
	for _, key := range status.SuspendedList {
		if key == expectedObscured {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("GetTokensStatus().SuspendedList expected to contain obscured key %q, got %v", expectedObscured, status.SuspendedList)
	}
}

func TestModelQuotaExceeded(t *testing.T) {
	_ = os.Remove(getQuotaExceededFilePath())
	_ = os.Remove(getSuspendedFilePath())
	defer func() {
		_ = os.Remove(getQuotaExceededFilePath())
		_ = os.Remove(getSuspendedFilePath())
	}()

	key := "AIzaSyDUMMY_KEY_FOR_MODEL_TEST_123"
	model := "gemini-3.6-flash"

	// 1. Initial check (no quota exceeded)
	if IsKeyModelQuotaExceeded(key, model) {
		t.Errorf("IsKeyModelQuotaExceeded expected false, got true")
	}

	// 2. Add quota exceeded for specific model
	if err := AddQuotaExceededKeyAndModel(key, model, 2*time.Hour); err != nil {
		t.Fatalf("AddQuotaExceededKeyAndModel failed: %v", err)
	}

	// 3. Verify specific model is exceeded
	if !IsKeyModelQuotaExceeded(key, model) {
		t.Errorf("IsKeyModelQuotaExceeded for model expected true, got false")
	}

	// 4. Verify another model is NOT exceeded
	if IsKeyModelQuotaExceeded(key, "gemini-3.5-flash") {
		t.Errorf("IsKeyModelQuotaExceeded for different model expected false, got true")
	}

	// 5. Verify that IsKeyAllModelsQuotaExceeded returns false initially because only one model is exceeded
	if IsKeyAllModelsQuotaExceeded(key) {
		t.Errorf("IsKeyAllModelsQuotaExceeded expected false (only one model exceeded), got true")
	}

	// 6. Add quota exceeded for all DefaultModels
	for _, m := range DefaultModels {
		if err := AddQuotaExceededKeyAndModel(key, m, 2*time.Hour); err != nil {
			t.Fatalf("AddQuotaExceededKeyAndModel failed for %s: %v", m, err)
		}
	}

	// 7. Verify IsKeyAllModelsQuotaExceeded is now true
	if !IsKeyAllModelsQuotaExceeded(key) {
		t.Errorf("IsKeyAllModelsQuotaExceeded expected true (all models exceeded), got false")
	}
}

func TestModelQuotaExceededFallbackAndLegacy(t *testing.T) {
	_ = os.Remove(getQuotaExceededFilePath())
	_ = os.Remove(getSuspendedFilePath())
	defer func() {
		_ = os.Remove(getQuotaExceededFilePath())
		_ = os.Remove(getSuspendedFilePath())
	}()

	keyFallback := "AIzaSyDUMMY_FALLBACK_KEY_888"

	// Test 1: model == "" fallback -> marks all default models as exceeded
	if err := AddQuotaExceededKeyAndModel(keyFallback, "", 1*time.Hour); err != nil {
		t.Fatalf("AddQuotaExceededKeyAndModel with empty model failed: %v", err)
	}

	for _, m := range DefaultModels {
		if !IsKeyModelQuotaExceeded(keyFallback, m) {
			t.Errorf("Expected model %s to be exceeded due to empty model fallback", m)
		}
	}

	// Test 2: Legacy fallback check -> key stored directly without colon suffix
	keyLegacy := "AIzaSyDUMMY_LEGACY_KEY_777"
	listMutex.Lock()
	list, err := loadQuotaExceededList()
	if err != nil {
		list = make(map[string]time.Time)
	}
	list[keyLegacy] = time.Now().Add(1 * time.Hour)
	_ = saveQuotaExceededList(list)
	listMutex.Unlock()

	// Verify IsKeyModelQuotaExceeded detects the legacy fallback for any model
	for _, m := range DefaultModels {
		if !IsKeyModelQuotaExceeded(keyLegacy, m) {
			t.Errorf("Expected model %s to be exceeded due to legacy fallback", m)
		}
	}
}

func TestGetTokensStatusDegraded(t *testing.T) {
	_ = os.Remove(getQuotaExceededFilePath())
	_ = os.Remove(getSuspendedFilePath())
	defer func() {
		_ = os.Remove(getQuotaExceededFilePath())
		_ = os.Remove(getSuspendedFilePath())
	}()

	// Mock TOKENSCRIPT_DIR environment variable
	tempDir, err := os.MkdirTemp("", "tokenscript-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	oldDir := os.Getenv("TOKENSCRIPT_DIR")
	defer os.Setenv("TOKENSCRIPT_DIR", oldDir)
	os.Setenv("TOKENSCRIPT_DIR", tempDir)

	// Create a mock token script
	scriptContent := `#!/bin/sh
state_file="` + tempDir + `/state"
if [ ! -f "$state_file" ]; then
  echo "AIzaSyDUMMY_HEALTHY_KEY_001"
  touch "$state_file"
else
  echo "AIzaSyDUMMY_DEGRADED_KEY_002"
  rm -f "$state_file"
fi
`
	scriptPath := tempDir + "/get-token"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write mock script: %v", err)
	}

	// AIzaSyDUMMY_DEGRADED_KEY_002 will be exceeded only for gemini-3.6-flash
	if err := AddQuotaExceededKeyAndModel("AIzaSyDUMMY_DEGRADED_KEY_002", "gemini-3.6-flash", 1*time.Hour); err != nil {
		t.Fatalf("AddQuotaExceededKeyAndModel failed: %v", err)
	}

	status, err := GetTokensStatus()
	if err != nil {
		t.Fatalf("GetTokensStatus failed: %v", err)
	}

	// AIzaSyDUMMY_HEALTHY_KEY_001 should be Active (Healthy)
	foundHealthy := false
	for _, k := range status.ActiveList {
		if k == "AIzaSyDU..." {
			foundHealthy = true
			break
		}
	}
	if !foundHealthy {
		t.Errorf("Expected ActiveList to contain 'AIzaSyDU...', got %v", status.ActiveList)
	}

	// AIzaSyDUMMY_DEGRADED_KEY_002 should be Active (Degraded)
	foundDegraded := false
	for _, k := range status.ActiveList {
		if k == "AIzaSyDU... (Degraded: exceeded gemini-3.6-flash)" {
			foundDegraded = true
			break
		}
	}
	if !foundDegraded {
		t.Errorf("Expected ActiveList to contain 'AIzaSyDU... (Degraded: exceeded gemini-3.6-flash)', got %v", status.ActiveList)
	}
}

func TestQuotaStreamTracker(t *testing.T) {
	t.Run("retry backoff in poll 1 and RESOURCE_EXHAUSTED in poll 2", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		fatal, transient := tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff... "))
		if fatal {
			t.Fatalf("Poll 1: expected fatal=false, got true")
		}
		if !transient {
			t.Fatalf("Poll 1: expected transient=true, got false")
		}

		fatal, _ = tracker.ObservePoll([]byte(`_ApiError: {"error":{"message":"RESOURCE_EXHAUSTED"}}` + "\n"))
		if fatal {
			t.Fatalf("Poll 2: expected fatal=false when RESOURCE_EXHAUSTED follows retry backoff from Poll 1, got true")
		}
	})

	t.Run("RESOURCE_EXHAUSTED in poll 1 and retry backoff in poll 2", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		fatal, _ := tracker.ObservePoll([]byte(`_ApiError: {"error":{"message":"RESOURCE_EXHAUSTED"}}` + "\n"))
		if fatal {
			t.Fatalf("Poll 1: expected fatal=false during 1-poll grace period, got true")
		}

		fatal, transient := tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff...\n"))
		if fatal {
			t.Fatalf("Poll 2: expected fatal=false when retry backoff arrives in Poll 2, got true")
		}
		if !transient {
			t.Fatalf("Poll 2: expected transient=true, got false")
		}
	})

	t.Run("mid-word split of Retrying with backoff across polls", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		fatal, _ := tracker.ObservePoll([]byte("Attempt 1 failed with status: 429. Retrying with "))
		if fatal {
			t.Fatalf("Poll 1: expected fatal=false during grace period, got true")
		}

		fatal, transient := tracker.ObservePoll([]byte(`backoff... _ApiError: {"error":{"message":"RESOURCE_EXHAUSTED"}}` + "\n"))
		if fatal {
			t.Fatalf("Poll 2: expected fatal=false after mid-word retry backoff completion, got true")
		}
		if !transient {
			t.Fatalf("Poll 2: expected transient=true, got false")
		}
	})

	t.Run("mid-word split of unambiguous fatal error across polls", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		fatal, _ := tracker.ObservePoll([]byte("Error: Quota exceeded for quota metric 'Generate requests per "))
		if fatal {
			t.Fatalf("Poll 1: expected fatal=false, got true")
		}

		fatal, _ = tracker.ObservePoll([]byte("day' and limit 'GenerateRequestsPerDayPerProject'\n"))
		if !fatal {
			t.Fatalf("Poll 2: expected fatal=true immediately on unambiguous RPD quota error, got false")
		}
	})

	t.Run("production googleapis 429 with exceeded your current quota and Retrying with backoff is not fatal", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		prodLog := `Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {"error":{"message":"{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"You exceeded your current quota, please check your plan and billing details.\",\n    \"status\": \"RESOURCE_EXHAUSTED\"\n  }\n}\n","code":429,"status":"Too Many Requests"}}` + "\n"
		fatal, transient := tracker.ObservePoll([]byte(prodLog))
		if fatal {
			t.Fatalf("expected fatal=false on production transient retry log, got true")
		}
		if !transient {
			t.Fatalf("expected transient=true on production transient retry log, got false")
		}
	})

	t.Run("unhandled ambiguous 429 confirmed fatal on next poll even if empty", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		fatal, _ := tracker.ObservePoll([]byte("Error: status: 429 Too Many Requests\n"))
		if fatal {
			t.Fatalf("Poll 1: expected fatal=false on first poll (grace period), got true")
		}

		fatal, _ = tracker.ObservePoll(nil)
		if !fatal {
			t.Fatalf("Poll 2: expected fatal=true after grace period elapsed with no retry backoff, got false")
		}
	})

	t.Run("unhandled ambiguous 429 confirmed fatal immediately on process exit", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		fatal, _ := tracker.ObservePoll([]byte(`_ApiError: {"error":{"message":"RESOURCE_EXHAUSTED"}}` + "\n"))
		if fatal {
			t.Fatalf("Poll 1: expected fatal=false during grace period, got true")
		}

		if !tracker.ObserveFinal(nil, true) {
			t.Fatalf("ObserveFinal: expected fatal=true when process exits with failure and pending quota error")
		}
	})

	t.Run("no window pollution: old retry does not mask subsequent unretried 429", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		// Poll 1: transient retry
		fatal, _ := tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {\"error\":{\"message\":\"RESOURCE_EXHAUSTED\"}}\n"))
		if fatal {
			t.Fatalf("Poll 1: expected fatal=false, got true")
		}

		// Poll 2: normal output followed by a new unretried 429 on a new line
		fatal, _ = tracker.ObservePoll([]byte("Doing work...\nError: status: 429 Too Many Requests\n"))
		if fatal {
			t.Fatalf("Poll 2: expected fatal=false on first poll of new error (grace period), got true")
		}

		// Poll 3: no retry arrives
		fatal, _ = tracker.ObservePoll([]byte("Process exiting...\n"))
		if !fatal {
			t.Fatalf("Poll 3: expected fatal=true for unretried 429 despite earlier retry in window, got false")
		}
	})

	t.Run("oversized poll chunk keeps the window bounded", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		// Simulate reattaching to a long-running task: the first tail reads the whole
		// backlog in one chunk, which is much larger than the sliding window.
		huge := bytes.Repeat([]byte("noise line that is perfectly normal output\n"), 4000)
		fatal, _ := tracker.ObservePoll(huge)
		if fatal {
			t.Fatalf("expected fatal=false for benign backlog, got true")
		}
		if got := len(tracker.Window()); got > DefaultQuotaWindowSize {
			t.Fatalf("window not bounded: got %d bytes, want <= %d", got, DefaultQuotaWindowSize)
		}
	})

	t.Run("oversized poll chunk still detects fatal marker in the discarded prefix", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		var chunk []byte
		chunk = append(chunk, []byte("Quota exceeded for metric: Generate requests per day\n")...)
		chunk = append(chunk, bytes.Repeat([]byte("trailing benign output\n"), 2000)...)
		if len(chunk) <= DefaultQuotaWindowSize {
			t.Fatalf("test setup: chunk must exceed the window size")
		}

		fatal, _ := tracker.ObservePoll(chunk)
		if !fatal {
			t.Fatalf("expected fatal=true for RPD exhaustion at the head of an oversized chunk, got false")
		}
	})

	t.Run("oversized poll chunk preserves grace-period offsets", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		chunk := append(bytes.Repeat([]byte("benign output line\n"), 2000),
			[]byte("Error: status: 429 Too Many Requests\n")...)
		fatal, _ := tracker.ObservePoll(chunk)
		if fatal {
			t.Fatalf("Poll 1: expected fatal=false during grace period, got true")
		}

		fatal, _ = tracker.ObservePoll(nil)
		if !fatal {
			t.Fatalf("Poll 2: expected fatal=true after grace period elapsed, got false")
		}
	})

	t.Run("steady-state polling is allocation-free", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()
		// Fill the window past maxWindowSize so every subsequent poll trims.
		tracker.ObservePoll(bytes.Repeat([]byte("normal task output line\n"), 400))
		delta := []byte("still working on the task...\n")

		if allocs := testing.AllocsPerRun(200, func() { tracker.ObservePoll(delta) }); allocs != 0 {
			t.Fatalf("ObservePoll allocated %v times per poll in steady state, want 0 (is trimWindow reslicing instead of shifting?)", allocs)
		}
	})

	t.Run("tracks model quota exhaustion across fallback loop when retries fail", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		// Poll 1: Model 1 starts
		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		if tracker.CurrentModel() != "gemini-3.7-flash" {
			t.Fatalf("expected CurrentModel to be gemini-3.7-flash, got %s", tracker.CurrentModel())
		}

		// Poll 2: Model 1 encounters 429 retries with backoff
		fatal, transient := tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {\"error\":{\"message\":\"RESOURCE_EXHAUSTED\"}}\n"))
		if fatal {
			t.Fatalf("expected fatal=false during retry backoff, got true")
		}
		if !transient {
			t.Fatalf("expected transient=true, got false")
		}

		// Poll 3: Model 1 fails and script falls back to Model 2
		tracker.ObservePoll([]byte("Engine execution failed with model: gemini-3.7-flash. Retrying with next model...\nTrying model: gemini-3.6-flash\n"))
		newlyExceeded := tracker.NewlyExceededModels()
		if len(newlyExceeded) != 1 || newlyExceeded[0] != "gemini-3.7-flash" {
			t.Fatalf("expected NewlyExceededModels to contain [gemini-3.7-flash], got %v", newlyExceeded)
		}
		tracker.AckExceededModels(newlyExceeded...)
		if tracker.CurrentModel() != "gemini-3.6-flash" {
			t.Fatalf("expected CurrentModel to be gemini-3.6-flash, got %s", tracker.CurrentModel())
		}

		// Poll 4: Model 2 encounters transient 429 but succeeds
		tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff...\nEngine execution successful with model: gemini-3.6-flash\n"))

		// Model 2 should not be exceeded
		if len(tracker.NewlyExceededModels()) != 0 {
			t.Fatalf("expected no new exceeded models after successful model run, got %v", tracker.NewlyExceededModels())
		}

		exceeded := tracker.ExceededModels()
		if len(exceeded) != 1 || exceeded[0] != "gemini-3.7-flash" {
			t.Fatalf("expected ExceededModels to be [gemini-3.7-flash], got %v", exceeded)
		}
	})

	t.Run("tracks model quota exhaustion when process fails after retries", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {\"error\":{\"message\":\"RESOURCE_EXHAUSTED\"}}\n"))

		// Process exits with failure (e.g. exit code 1)
		isFatal := tracker.ObserveFinal(nil, true)
		if !isFatal {
			t.Fatalf("expected ObserveFinal to report fatal quota when process failed after 429 retries")
		}

		exceeded := tracker.ExceededModels()
		if len(exceeded) != 1 || exceeded[0] != "gemini-3.7-flash" {
			t.Fatalf("expected ExceededModels to contain gemini-3.7-flash, got %v", exceeded)
		}
	})

	t.Run("preserves quota error state across same-model retries", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		// Attempt 1: Model encounters 429
		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {\"error\":{\"message\":\"RESOURCE_EXHAUSTED\"}}\n"))

		// Attempt 2: Same model is retried (e.g. bash loop retries same model)
		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		// Attempt 2 fails without emitting a new 429
		tracker.ObservePoll([]byte("Engine execution failed with model: gemini-3.7-flash. Retrying with next model...\nTrying model: gemini-2.5-flash\n"))

		exceeded := tracker.NewlyExceededModels()
		if len(exceeded) != 1 || exceeded[0] != "gemini-3.7-flash" {
			t.Fatalf("expected NewlyExceededModels to contain gemini-3.7-flash despite same-model retry, got %v", exceeded)
		}
	})

	t.Run("failure line does not clear quota error before ObserveFinal runs", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {\"error\":{\"message\":\"RESOURCE_EXHAUSTED\"}}\n"))
		tracker.ObservePoll([]byte("Engine execution failed with model: gemini-3.7-flash. Retrying with next model...\n"))

		// Process terminates on failure (no more models or fatal exit)
		isFatal := tracker.ObserveFinal(nil, true)
		if !isFatal {
			t.Fatalf("expected ObserveFinal to report fatal quota error after failure line, got false")
		}

		exceeded := tracker.ExceededModels()
		if len(exceeded) != 1 || exceeded[0] != "gemini-3.7-flash" {
			t.Fatalf("expected ExceededModels to contain gemini-3.7-flash, got %v", exceeded)
		}
	})

	t.Run("handles single line with Trying model and quota error correctly", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		// Single line starting the model and reporting a 429 error
		tracker.ObservePoll([]byte("Trying model: gemini-2.5-pro - failed with status 429 RESOURCE_EXHAUSTED\n"))
		if tracker.CurrentModel() != "gemini-2.5-pro" {
			t.Fatalf("expected CurrentModel to be gemini-2.5-pro, got %s", tracker.CurrentModel())
		}

		tracker.ObservePoll([]byte("Engine execution failed with model: gemini-2.5-pro. Retrying with next model...\nTrying model: gemini-2.5-flash\n"))
		exceeded := tracker.NewlyExceededModels()
		if len(exceeded) != 1 || exceeded[0] != "gemini-2.5-pro" {
			t.Fatalf("expected gemini-2.5-pro to be marked quota exceeded, got %v", exceeded)
		}
	})

	t.Run("extracts model names with underscores and slashes", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		tracker.ObservePoll([]byte("Trying model: models/gemini-1.5-flash_preview\n"))
		if tracker.CurrentModel() != "models/gemini-1.5-flash_preview" {
			t.Fatalf("expected CurrentModel to be models/gemini-1.5-flash_preview, got %s", tracker.CurrentModel())
		}

		tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {\"error\":{\"message\":\"RESOURCE_EXHAUSTED\"}}\n"))
		tracker.ObservePoll([]byte("Engine execution failed with model: models/gemini-1.5-flash_preview. Retrying with next model...\nTrying model: models/gemini-1.5-pro_preview\n"))

		exceeded := tracker.NewlyExceededModels()
		if len(exceeded) != 1 || exceeded[0] != "models/gemini-1.5-flash_preview" {
			t.Fatalf("expected NewlyExceededModels to contain models/gemini-1.5-flash_preview, got %v", exceeded)
		}
	})

	t.Run("retains pending exceeded models until acknowledged", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		tracker.ObservePoll([]byte("429 RESOURCE_EXHAUSTED\n"))
		tracker.ObservePoll([]byte("Engine execution failed with model: gemini-3.7-flash. Retrying with next model...\nTrying model: gemini-2.5-flash\n"))

		// First poll / check: key was missing, caller does not call AckExceededModels
		pending1 := tracker.NewlyExceededModels()
		if len(pending1) != 1 || pending1[0] != "gemini-3.7-flash" {
			t.Fatalf("expected NewlyExceededModels to return [gemini-3.7-flash], got %v", pending1)
		}

		// Second poll / check: pending list must still retain gemini-3.7-flash
		pending2 := tracker.NewlyExceededModels()
		if len(pending2) != 1 || pending2[0] != "gemini-3.7-flash" {
			t.Fatalf("expected pending models to be retained when unacknowledged, got %v", pending2)
		}

		// Acknowledging removes the model
		tracker.AckExceededModels(pending2...)
		pending3 := tracker.NewlyExceededModels()
		if len(pending3) != 0 {
			t.Fatalf("expected no pending models after AckExceededModels, got %v", pending3)
		}
	})

	t.Run("unrelated process failure after successful model execution does not report fatal quota", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		tracker.ObservePoll([]byte("Model gemini-3.7-flash: Attempt 1 failed with status 429. Retrying with backoff...\n"))
		tracker.ObservePoll([]byte("Gemini execution successful with model: gemini-3.7-flash\n"))
		tracker.ObservePoll([]byte("Running tests...\n--- FAIL: TestFoo (0.00s)\n"))

		isFatal := tracker.ObserveFinal(nil, true)
		if isFatal {
			t.Fatalf("expected ObserveFinal to report fatal=false when process failed after model execution succeeded, got true")
		}

		if len(tracker.ExceededModels()) != 0 {
			t.Fatalf("expected no exceeded models when model succeeded, got %v", tracker.ExceededModels())
		}
	})

	t.Run("transient attempt failure line does not trigger model failure detection", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		tracker.ObservePoll([]byte("Model gemini-3.7-flash: Attempt 1 failed with status 429. Retrying with backoff...\n"))

		if len(tracker.NewlyExceededModels()) != 0 {
			t.Fatalf("expected no newly exceeded models on transient retry line, got %v", tracker.NewlyExceededModels())
		}
	})

	t.Run("case-insensitive model lifecycle markers", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		// Lowercase trying model
		tracker.ObservePoll([]byte("trying model: gemini-2.5-flash\n"))
		if tracker.CurrentModel() != "gemini-2.5-flash" {
			t.Fatalf("expected CurrentModel to be gemini-2.5-flash, got %s", tracker.CurrentModel())
		}

		tracker.ObservePoll([]byte("status: 429 RESOURCE_EXHAUSTED\n"))
		// Uppercase execution failed
		tracker.ObservePoll([]byte("ENGINE EXECUTION FAILED WITH MODEL: gemini-2.5-flash. Retrying with next model...\n"))

		exceeded := tracker.NewlyExceededModels()
		if len(exceeded) != 1 || exceeded[0] != "gemini-2.5-flash" {
			t.Fatalf("expected gemini-2.5-flash to be marked quota exceeded with uppercase failed marker, got %v", exceeded)
		}
		tracker.AckExceededModels(exceeded...)

		// Mixed case trying model and execution successful
		tracker.ObservePoll([]byte("Trying Model: gemini-2.5-pro\n"))
		if tracker.CurrentModel() != "gemini-2.5-pro" {
			t.Fatalf("expected CurrentModel to be gemini-2.5-pro, got %s", tracker.CurrentModel())
		}
		tracker.ObservePoll([]byte("Engine Execution Successful With Model: gemini-2.5-pro\n"))
		if len(tracker.NewlyExceededModels()) != 0 {
			t.Fatalf("expected no newly exceeded models after mixed case successful execution, got %v", tracker.NewlyExceededModels())
		}
	})

	t.Run("bytesContainsIgnoreCase handles uppercase and mixed-case substrings", func(t *testing.T) {
		tests := []struct {
			s        string
			sub      string
			expected bool
		}{
			{"hello world", "WORLD", true},
			{"HELLO WORLD", "world", true},
			{"foo BAR baz", "Bar", true},
			{"abc", "abcd", false},
			{"abc", "", true},
			{"", "a", false},
			{"trying model: gemini-2.5", "TRYING MODEL:", true},
		}

		for _, tc := range tests {
			if got := bytesContainsIgnoreCase([]byte(tc.s), []byte(tc.sub)); got != tc.expected {
				t.Errorf("bytesContainsIgnoreCase(%q, %q) = %v, want %v", tc.s, tc.sub, got, tc.expected)
			}
		}
	})

	t.Run("pendingLine is bounded to maxWindowSize across newline-less chunks", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()
		tracker.maxWindowSize = 100

		// Send 10 chunks of 50 bytes without newline
		chunk := bytes.Repeat([]byte("a"), 50)
		for i := 0; i < 10; i++ {
			tracker.ObservePoll(chunk)
		}

		if len(tracker.pendingLine) > tracker.maxWindowSize {
			t.Fatalf("pendingLine length %d exceeded maxWindowSize %d", len(tracker.pendingLine), tracker.maxWindowSize)
		}
	})

	t.Run("fallback loop transition does not trigger fatal ObservePoll on next model", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		// Model 1 starts, encounters 429 and RPD, and fails
		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		tracker.ObservePoll([]byte("Generate requests per day status 429 RESOURCE_EXHAUSTED\n"))
		tracker.ObservePoll([]byte("Engine execution failed with model: gemini-3.7-flash. Retrying with next model...\n"))

		// Model 2 starts
		tracker.ObservePoll([]byte("Trying model: gemini-3.6-flash\n"))

		// On subsequent poll tick while Model 2 is running, ObservePoll should NOT report fatal error
		isFatal, _ := tracker.ObservePoll([]byte("Working on task with model gemini-3.6-flash...\n"))
		if isFatal {
			t.Fatalf("expected ObservePoll to return fatal=false while Model 2 is running, got true")
		}

		// Model 1 should be marked quota exceeded
		exceeded := tracker.ExceededModels()
		if len(exceeded) != 1 || exceeded[0] != "gemini-3.7-flash" {
			t.Fatalf("expected ExceededModels to contain gemini-3.7-flash, got %v", exceeded)
		}
	})

	t.Run("key suspension error triggers fatal ObservePoll during model attempt", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		isFatal, _ := tracker.ObservePoll([]byte("API key suspended: CONSUMER_SUSPENDED\n"))
		if !isFatal {
			t.Fatalf("expected ObservePoll to return fatal=true on suspended key, got false")
		}
	})

	t.Run("model 1 quota failure followed by model 2 success and process failure does not report fatal quota", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		// Model 1 fails with quota
		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {\"error\":{\"message\":\"RESOURCE_EXHAUSTED\"}}\n"))
		tracker.ObservePoll([]byte("Engine execution failed with model: gemini-3.7-flash. Retrying with next model...\n"))

		// Model 2 succeeds
		tracker.ObservePoll([]byte("Trying model: gemini-3.6-flash\n"))
		tracker.ObservePoll([]byte("Engine execution successful with model: gemini-3.6-flash\n"))

		// Post-engine step (e.g. git push or tests) fails
		tracker.ObservePoll([]byte("git push failed\n"))

		isFatal := tracker.ObserveFinal(nil, true)
		if isFatal {
			t.Fatalf("expected ObserveFinal to report fatal=false when fallback model succeeded, got true")
		}

		exceeded := tracker.ExceededModels()
		if len(exceeded) != 1 || exceeded[0] != "gemini-3.7-flash" {
			t.Fatalf("expected only gemini-3.7-flash to be marked exceeded, got %v", exceeded)
		}
	})

	t.Run("model 1 quota failure followed by model 2 non-quota failure does not report fatal quota", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		// Model 1 fails with quota
		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))
		tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff... _ApiError: {\"error\":{\"message\":\"RESOURCE_EXHAUSTED\"}}\n"))
		tracker.ObservePoll([]byte("Engine execution failed with model: gemini-3.7-flash. Retrying with next model...\n"))

		// Model 2 fails for syntax / non-quota reason
		tracker.ObservePoll([]byte("Trying model: gemini-3.6-flash\n"))
		tracker.ObservePoll([]byte("syntax error: unexpected token\n"))

		isFatal := tracker.ObserveFinal(nil, true)
		if isFatal {
			t.Fatalf("expected ObserveFinal to report fatal=false when fallback model failed without quota errors, got true")
		}

		exceeded := tracker.ExceededModels()
		if len(exceeded) != 1 || exceeded[0] != "gemini-3.7-flash" {
			t.Fatalf("expected only gemini-3.7-flash to be marked exceeded, got %v", exceeded)
		}
	})

	t.Run("ObservePoll hasTransient only reports true on poll tick receiving retry message", func(t *testing.T) {
		tracker := NewQuotaStreamTracker()

		tracker.ObservePoll([]byte("Trying model: gemini-3.7-flash\n"))

		// Tick 1: retry backoff message received -> transient is true
		_, isTransient := tracker.ObservePoll([]byte("Attempt 1 failed with status 429. Retrying with backoff...\n"))
		if !isTransient {
			t.Fatalf("expected isTransient=true on poll tick with retry backoff message, got false")
		}

		// Tick 2: ordinary output -> transient is false (not sticky from buffer)
		_, isTransient = tracker.ObservePoll([]byte("Still waiting for agent response...\n"))
		if isTransient {
			t.Fatalf("expected isTransient=false on subsequent poll tick without new retry message, got true")
		}
	})
}

// BenchmarkQuotaStreamTrackerObservePoll exercises the steady-state hot path: a full
// 8KB window scanned on every poll tick. Matching on []byte keeps this allocation-free.
func BenchmarkQuotaStreamTrackerObservePoll(b *testing.B) {
	tracker := NewQuotaStreamTracker()
	tracker.ObservePoll(bytes.Repeat([]byte("normal task output line\n"), 400))
	delta := []byte("still working on the task...\n")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tracker.ObservePoll(delta)
	}
}
