package tokens

import (
	"os"
	"testing"
)

func TestPickKey(t *testing.T) {
	keys := `key1
key2
key3`
	
	// Test that it picks one of the keys
	found := make(map[string]bool)
	for i := 0; i < 20; i++ {
		key := PickKey(keys, "seed")
		if key != "key1" && key != "key2" && key != "key3" {
			t.Errorf("Picked invalid key: %q", key)
		}
		found[key] = true
	}
    // We expect to find more than 1 key usually, but with a fixed seed and small iterations it might not.
    // However, our PickKey uses time.Now().UnixNano() too.
}

func TestGetGeminiAPIKey_MultipleKeys(t *testing.T) {
	originalEnv := os.Getenv("GEMINI_API_KEY")
	defer os.Setenv("GEMINI_API_KEY", originalEnv)

	keys := `key1
key2
key3`
	os.Setenv("GEMINI_API_KEY", keys)

	key, err := GetGeminiAPIKey("seed1")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if key != "key1" && key != "key2" && key != "key3" {
		t.Errorf("Expected one of the keys, got %q", key)
	}
}

func TestGetRawGeminiAPIKey(t *testing.T) {
	originalEnv := os.Getenv("GEMINI_API_KEY")
	defer os.Setenv("GEMINI_API_KEY", originalEnv)

	keys := `key1
key2
key3`
	os.Setenv("GEMINI_API_KEY", keys)

	key, err := GetRawGeminiAPIKey("seed1")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if key != keys {
		t.Errorf("Expected raw keys %q, got %q", keys, key)
	}
}

func TestRotation(t *testing.T) {
	keys := `key1
key2
key3
key4
key5`
	
	results := make(map[string]int)
	for i := 0; i < 100; i++ {
		key := PickKey(keys, "seed")
		results[key]++
	}
	
	t.Logf("Rotation results: %v", results)
	if len(results) <= 1 {
		t.Errorf("Rotation failed, only got keys: %v", results)
	}
}
