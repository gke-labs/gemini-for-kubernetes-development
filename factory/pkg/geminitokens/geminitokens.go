package geminitokens

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const KeyGeminiAPIKey = "GEMINI_API_KEY"

var (
	listMutex      sync.Mutex
	suspendedMutex sync.Mutex
	keyRegexp      = regexp.MustCompile(`api_key:([A-Za-z0-9_\-\.]{25,})|(AIzaSy[A-Za-z0-9_\-]{33})|(AQ\.[A-Za-z0-9_\-]{40,})`)
)

func getQuotaExceededFilePath() string {
	// 1. If FACTORY_CONFIG or .factory.cfg is used, put it in the same directory
	configPath := ""
	if _, err := os.Stat(".factory.cfg"); err == nil {
		configPath = ".factory.cfg"
	} else if envVal := os.Getenv("FACTORY_CONFIG"); envVal != "" {
		fi, err := os.Stat(envVal)
		if err == nil {
			if fi.IsDir() {
				configPath = filepath.Join(envVal, ".factory.cfg")
			} else {
				configPath = envVal
			}
		}
	}
	if configPath != "" {
		absPath, err := filepath.Abs(configPath)
		if err == nil {
			return filepath.Join(filepath.Dir(absPath), ".factory_quota_exceeded_keys.json")
		}
	}

	cwd, err := os.Getwd()
	if err == nil {
		return filepath.Join(cwd, ".factory_quota_exceeded_keys.json")
	}
	return ".factory_quota_exceeded_keys.json"
}

func getSuspendedFilePath() string {
	configPath := ""
	if _, err := os.Stat(".factory.cfg"); err == nil {
		configPath = ".factory.cfg"
	} else if envVal := os.Getenv("FACTORY_CONFIG"); envVal != "" {
		fi, err := os.Stat(envVal)
		if err == nil {
			if fi.IsDir() {
				configPath = filepath.Join(envVal, ".factory.cfg")
			} else {
				configPath = envVal
			}
		}
	}
	if configPath != "" {
		absPath, err := filepath.Abs(configPath)
		if err == nil {
			return filepath.Join(filepath.Dir(absPath), ".factory_suspended_keys.json")
		}
	}

	cwd, err := os.Getwd()
	if err == nil {
		return filepath.Join(cwd, ".factory_suspended_keys.json")
	}
	return ".factory_suspended_keys.json"
}

func loadQuotaExceededList() (map[string]time.Time, error) {
	filePath := getQuotaExceededFilePath()
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]time.Time), nil
		}
		return nil, err
	}

	var rawMap map[string]string
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return nil, err
	}

	list := make(map[string]time.Time)
	for k, v := range rawMap {
		t, err := time.Parse(time.RFC3339, v)
		if err == nil {
			list[k] = t
		}
	}
	return list, nil
}

func saveQuotaExceededList(list map[string]time.Time) error {
	filePath := getQuotaExceededFilePath()
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}

	rawMap := make(map[string]string)
	for k, v := range list {
		rawMap[k] = v.Format(time.RFC3339)
	}

	data, err := json.MarshalIndent(rawMap, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0644)
}

func loadSuspendedList() (map[string]string, error) {
	filePath := getSuspendedFilePath()
	result := make(map[string]string)

	if data, err := os.ReadFile(filePath); err == nil {
		var rawMap map[string]string
		if err := json.Unmarshal(data, &rawMap); err == nil {
			for k, v := range rawMap {
				result[k] = v
			}
		}
	}

	// Just load from the primary path
	return result, nil
}

func saveSuspendedList(list map[string]string) error {
	filePath := getSuspendedFilePath()
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0644)
}

func IsKeySuspended(key string) bool {
	if key == "" {
		return false
	}
	suspendedMutex.Lock()
	defer suspendedMutex.Unlock()

	list, err := loadSuspendedList()
	if err != nil {
		return false
	}
	_, exists := list[key]
	return exists
}

func AddSuspendedKey(key string) error {
	if key == "" {
		return nil
	}
	suspendedMutex.Lock()
	defer suspendedMutex.Unlock()

	list, err := loadSuspendedList()
	if err != nil {
		list = make(map[string]string)
	}
	list[key] = time.Now().Format(time.RFC3339)

	keyDesc := key
	if len(keyDesc) > 8 {
		keyDesc = keyDesc[:8] + "..."
	}
	klog.Warningf("Permanently marking key starting with '%s' as SUSPENDED (CONSUMER_SUSPENDED)", keyDesc)

	if err := saveSuspendedList(list); err != nil {
		klog.Errorf("Failed to save suspended keys list: %v", err)
	}

	// Also mark it in quota exceeded list with 100-year expiration as fallback guardrail
	_ = AddQuotaExceededKey(key, 100*365*24*time.Hour)
	return nil
}

func IsKeyQuotaExceeded(key string) bool {
	if key == "" {
		return false
	}

	listMutex.Lock()
	defer listMutex.Unlock()

	list, err := loadQuotaExceededList()
	if err != nil {
		klog.Errorf("Failed to load quota exceeded list: %v", err)
		return false
	}

	exp, exists := list[key]
	if !exists {
		return false
	}

	if time.Now().After(exp) {
		// Clean up expired entry
		delete(list, key)
		_ = saveQuotaExceededList(list)
		return false
	}

	return true
}

func AddQuotaExceededKey(key string, duration time.Duration) error {
	if key == "" {
		return nil
	}

	listMutex.Lock()
	defer listMutex.Unlock()

	list, err := loadQuotaExceededList()
	if err != nil {
		return fmt.Errorf("loading quota exceeded list: %w", err)
	}

	expiration := time.Now().Add(duration)
	list[key] = expiration

	keyDesc := key
	if len(keyDesc) > 8 {
		keyDesc = keyDesc[:8] + "..."
	}
	klog.Infof("Adding key starting with '%s' to quota exceeded list until %s (%s)", keyDesc, expiration.Format(time.RFC3339), duration)

	if err := saveQuotaExceededList(list); err != nil {
		return fmt.Errorf("saving quota exceeded list: %w", err)
	}
	return nil
}

// IsSuspendedKeyError checks if the output indicates that an API key has been suspended or disabled permanently.
func IsSuspendedKeyError(data []byte) bool {
	str := string(data)
	return strings.Contains(str, "CONSUMER_SUSPENDED") ||
		strings.Contains(str, "has been suspended") ||
		strings.Contains(str, "API_KEY_INVALID") ||
		strings.Contains(str, "API key not valid") ||
		(strings.Contains(str, "PERMISSION_DENIED") && (strings.Contains(str, "suspended") || strings.Contains(str, "disabled")))
}

// ExtractAPIKeyFromError attempts to parse an explicit API key string from error logs/payloads.
func ExtractAPIKeyFromError(data []byte) string {
	matches := keyRegexp.FindSubmatch(data)
	if len(matches) > 0 {
		for _, m := range matches[1:] {
			if len(m) > 0 {
				return string(m)
			}
		}
	}
	return ""
}

// IsFatalQuotaError checks if the output indicates daily quota exhaustion (RPD) or unrecoverable billing/quota errors,
// ignoring intermediate transient RPM/TPM retry messages ("Retrying with backoff").
func IsFatalQuotaError(data []byte) bool {
	if IsSuspendedKeyError(data) {
		return true
	}

	str := string(data)

	hasFatalKeyword := strings.Contains(str, "exceeded your current quota") ||
		strings.Contains(str, "check your plan and billing details") ||
		strings.Contains(str, "requests per day") ||
		strings.Contains(str, "Generate requests per day") ||
		strings.Contains(str, "Max retries exceeded") ||
		strings.Contains(str, "Terminal error")

	if hasFatalKeyword {
		return true
	}

	// If the log indicates an active retry with backoff, treat as transient rate limit rather than fatal RPD quota.
	if strings.Contains(str, "Retrying with backoff") {
		return false
	}

	return strings.Contains(str, "RESOURCE_EXHAUSTED") ||
		strings.Contains(str, "status: 429") ||
		strings.Contains(str, "statusCode: 429") ||
		strings.Contains(str, "status\": 429") ||
		strings.Contains(str, "status: \"Too Many Requests\"")
}

// IsTransientRateLimit checks if the output indicates a transient RPM/TPM rate limit spike being retried.
func IsTransientRateLimit(data []byte) bool {
	str := string(data)
	return strings.Contains(str, "Retrying with backoff") ||
		(strings.Contains(str, "status: 429") && !IsFatalQuotaError(data))
}

func ContainsQuotaError(data []byte) bool {
	return IsFatalQuotaError(data)
}

func GetGeminiAPIKey(secret *corev1.Secret) string {
	if token, err := getTokenFromScript(); err == nil && token != "" {
		return token
	}
	if secret != nil {
		return string(secret.Data[KeyGeminiAPIKey])
	}
	return ""
}

func getTokenFromScript() (string, error) {
	dir := os.Getenv("TOKENSCRIPT_DIR")
	if dir == "" {
		return "", nil
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("failed to read tokenscript dir: %w", err)
	}

	var scriptPath string
	for _, f := range files {
		if f.IsDir() || strings.HasPrefix(f.Name(), "..") {
			continue
		}
		scriptPath = filepath.Join(dir, f.Name())
		break
	}

	if scriptPath == "" {
		return "", nil
	}

	// Try up to 30 times to get a non-quota-exceeded and non-suspended token from the script
	for attempt := 0; attempt < 30; attempt++ {
		cmd := exec.Command(scriptPath)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to run tokenscript %s: %w", scriptPath, err)
		}

		token := strings.TrimSpace(out.String())
		if token == "" {
			return "", nil
		}

		if !IsKeyQuotaExceeded(token) && !IsKeySuspended(token) {
			return token, nil
		}
	}

	return "", fmt.Errorf("failed to find a non-quota-exceeded token from script after 30 attempts")
}

func TimeUntilNextMidnight() time.Duration {
	now := time.Now()
	nextMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	return nextMidnight.Sub(now)
}

func DiscoverTokensFromScript() ([]string, error) {
	dir := os.Getenv("TOKENSCRIPT_DIR")
	if dir == "" {
		return nil, nil
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read tokenscript dir: %w", err)
	}

	var scriptPath string
	for _, f := range files {
		if f.IsDir() || strings.HasPrefix(f.Name(), "..") {
			continue
		}
		scriptPath = filepath.Join(dir, f.Name())
		break
	}

	if scriptPath == "" {
		return nil, nil
	}

	uniqueTokens := make(map[string]bool)
	// Run the script 100 times to collect all unique tokens
	for i := 0; i < 100; i++ {
		cmd := exec.Command(scriptPath)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			token := strings.TrimSpace(out.String())
			if token != "" {
				uniqueTokens[token] = true
			}
		}
	}

	tokens := make([]string, 0, len(uniqueTokens))
	for t := range uniqueTokens {
		tokens = append(tokens, t)
	}
	return tokens, nil
}

type TokensStatus struct {
	Total             int
	QuotaExceeded     int
	Suspended         int
	Active            int
	ActiveList        []string
	QuotaExceededList []string
	SuspendedList     []string
}

func GetTokensStatus() (*TokensStatus, error) {
	allTokens, err := DiscoverTokensFromScript()
	if err != nil {
		return nil, err
	}

	status := &TokensStatus{}
	seenSuspended := make(map[string]bool)

	suspendedMap, _ := loadSuspendedList()
	for k := range suspendedMap {
		if k != "" {
			seenSuspended[k] = true
		}
	}

	for _, t := range allTokens {
		obscured := t
		if len(obscured) > 8 {
			obscured = obscured[:8] + "..."
		}
		if IsKeySuspended(t) || seenSuspended[t] {
			seenSuspended[t] = true
			status.SuspendedList = append(status.SuspendedList, t) // Full un-obscured key string
		} else if IsKeyQuotaExceeded(t) {
			status.QuotaExceededList = append(status.QuotaExceededList, obscured)
		} else {
			status.ActiveList = append(status.ActiveList, obscured)
		}
	}

	// Add any suspended keys from storage file that weren't sampled in allTokens
	for k := range suspendedMap {
		if k != "" {
			found := false
			for _, s := range status.SuspendedList {
				if s == k {
					found = true
					break
				}
			}
			if !found {
				status.SuspendedList = append(status.SuspendedList, k)
			}
		}
	}

	status.Suspended = len(status.SuspendedList)
	status.QuotaExceeded = len(status.QuotaExceededList)
	status.Active = len(status.ActiveList)
	status.Total = status.Suspended + status.QuotaExceeded + status.Active
	return status, nil
}
