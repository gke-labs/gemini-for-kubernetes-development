package geminitokens

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const KeyGeminiAPIKey = "GEMINI_API_KEY"

var listMutex sync.Mutex

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

	// 2. Otherwise use user config dir
	dir, err := os.UserConfigDir()
	if err == nil {
		return filepath.Join(dir, "factory", "quota_exceeded_keys.json")
	}

	// 3. Fallback to /tmp
	return "/tmp/.factory_quota_exceeded_keys.json"
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

func ContainsQuotaError(data []byte) bool {
	str := string(data)
	return strings.Contains(str, "RESOURCE_EXHAUSTED") ||
		strings.Contains(str, "exceeded your current quota") ||
		strings.Contains(str, "status: 429") ||
		strings.Contains(str, "statusCode: 429") ||
		strings.Contains(str, "status\": 429") ||
		strings.Contains(str, "status: \"Too Many Requests\"")
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

	// Try up to 30 times to get a non-quota-exceeded token from the script
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

		if !IsKeyQuotaExceeded(token) {
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
	Active            int
	ActiveList        []string
	QuotaExceededList []string
}

func GetTokensStatus() (*TokensStatus, error) {
	allTokens, err := DiscoverTokensFromScript()
	if err != nil {
		return nil, err
	}

	if len(allTokens) == 0 {
		return nil, nil
	}

	status := &TokensStatus{}
	for _, t := range allTokens {
		obscured := t
		if len(obscured) > 8 {
			obscured = obscured[:8] + "..."
		}
		if IsKeyQuotaExceeded(t) {
			status.QuotaExceeded++
			status.QuotaExceededList = append(status.QuotaExceededList, obscured)
		} else {
			status.Active++
			status.ActiveList = append(status.ActiveList, obscured)
		}
	}
	status.Total = len(allTokens)
	return status, nil
}
