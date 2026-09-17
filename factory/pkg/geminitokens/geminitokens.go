package geminitokens

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

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

	dir, err := os.UserConfigDir()
	if err == nil {
		return filepath.Join(dir, "factory", "quota_exceeded_keys.json")
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

	dir, err := os.UserConfigDir()
	if err == nil {
		return filepath.Join(dir, "factory", "suspended_keys.json")
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

	fallbackPaths := []string{}
	if userDir, err := os.UserConfigDir(); err == nil {
		fallbackPaths = append(fallbackPaths, filepath.Join(userDir, "factory", "suspended_keys.json"))
	}
	if cwd, err := os.Getwd(); err == nil {
		fallbackPaths = append(fallbackPaths, filepath.Join(cwd, ".factory_suspended_keys.json"))
	}
	for _, fp := range fallbackPaths {
		if fp != filePath {
			if data, err := os.ReadFile(fp); err == nil {
				var rawMap map[string]string
				if err := json.Unmarshal(data, &rawMap); err == nil {
					for k, v := range rawMap {
						result[k] = v
					}
				}
			}
		}
	}

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

	return nil
}

// DefaultModels is the list of Gemini models to check when verifying general token health.
var DefaultModels = []string{
	"gemini-3.7-flash",
	"gemini-3.6-flash",
	"gemini-3.5-flash",
	"gemini-3-flash-preview",
	"gemini-3.1-pro-preview",
	"gemini-2.5-pro",
}

// GetAvailableModels returns the list of models (from the given candidates) whose quota is not exceeded for the key, loading the quota list exactly once.
func GetAvailableModels(key string, models []string) []string {
	if key == "" {
		return models
	}

	listMutex.Lock()
	list, err := loadQuotaExceededList()
	listMutex.Unlock()
	if err != nil {
		klog.Errorf("Failed to load quota exceeded list: %v", err)
		return models
	}

	var active []string
	now := time.Now()
	for _, m := range models {
		mapKey := key + ":" + m

		// Check model-specific quota
		exp, exists := list[mapKey]
		if exists && !now.After(exp) {
			// quota is exceeded for this model, so we skip it
			continue
		}

		// Fallback/Legacy check: if key itself exists in list without colon, it means general/all models quota is exceeded
		expLegacy, legacyExists := list[key]
		if legacyExists && !now.After(expLegacy) {
			// legacy quota is exceeded for the key itself, so all models are skipped
			continue
		}

		active = append(active, m)
	}

	return active
}

// IsKeyAllModelsQuotaExceeded checks if all DefaultModels have exceeded quota for the given key.
func IsKeyAllModelsQuotaExceeded(key string) bool {
	if key == "" {
		return false
	}
	active := GetAvailableModels(key, DefaultModels)
	return len(active) == 0
}

func IsKeyModelQuotaExceeded(key string, model string) bool {
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

	modified := false
	defer func() {
		if modified {
			_ = saveQuotaExceededList(list)
		}
	}()

	if model != "" {
		mapKey := key + ":" + model
		if exp, exists := list[mapKey]; exists {
			if time.Now().After(exp) {
				delete(list, mapKey)
				modified = true
				return false
			}
			return true
		}
	}

	// Legacy fallback/general check: if key exists directly without colon suffix, treat it as quota exceeded for all models.
	if exp, exists := list[key]; exists {
		if time.Now().After(exp) {
			delete(list, key)
			modified = true
			return false
		}
		return true
	}

	return false
}

func AddQuotaExceededKeyAndModel(key string, model string, duration time.Duration) error {
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

	keyDesc := key
	if len(keyDesc) > 8 {
		keyDesc = keyDesc[:8] + "..."
	}

	if model == "" {
		for _, m := range DefaultModels {
			mapKey := key + ":" + m
			list[mapKey] = expiration
		}
		klog.Infof("Adding key starting with '%s' for ALL models (fallback because model is empty) to quota exceeded list until %s (%s)", keyDesc, expiration.Format(time.RFC3339), duration)
	} else {
		mapKey := key + ":" + model
		list[mapKey] = expiration
		klog.Infof("Adding key starting with '%s' for model '%s' to quota exceeded list until %s (%s)", keyDesc, model, expiration.Format(time.RFC3339), duration)
	}

	if err := saveQuotaExceededList(list); err != nil {
		return fmt.Errorf("saving quota exceeded list: %w", err)
	}
	return nil
}

// Log markers are stored as []byte rather than string so that the detection helpers
// below can match against raw log chunks with bytes.Contains/bytes.Index. These run on
// every poll tick over the whole rolling window, so converting the chunk to a string
// would copy the entire window on each call.
var (
	suspendedKeyMarkers = [][]byte{
		[]byte("CONSUMER_SUSPENDED"),
		[]byte("has been suspended"),
		[]byte("API_KEY_INVALID"),
		[]byte("API key not valid"),
	}
	permissionDeniedMarker = []byte("PERMISSION_DENIED")
	suspendedWordMarker    = []byte("suspended")
	disabledWordMarker     = []byte("disabled")
)

// IsSuspendedKeyError checks if the output indicates that an API key has been suspended or disabled permanently.
func IsSuspendedKeyError(data []byte) bool {
	for _, marker := range suspendedKeyMarkers {
		if bytes.Contains(data, marker) {
			return true
		}
	}
	return bytes.Contains(data, permissionDeniedMarker) &&
		(bytes.Contains(data, suspendedWordMarker) || bytes.Contains(data, disabledWordMarker))
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

// ambiguousQuotaMarkers are HTTP 429 / RESOURCE_EXHAUSTED indicators that are only fatal
// when they are not accompanied by a nearby "Retrying with backoff" message.
var ambiguousQuotaMarkers = [][]byte{
	[]byte("RESOURCE_EXHAUSTED"),
	[]byte("exceeded your current quota"),
	[]byte("check your plan and billing details"),
	[]byte("status: 429"),
	[]byte("statusCode: 429"),
	[]byte(`status": 429`),
	[]byte(`status: "Too Many Requests"`),
}

// unambiguousFatalMarkers indicate quota exhaustion or terminal failures that no amount
// of client-side backoff can recover from.
var unambiguousFatalMarkers = [][]byte{
	[]byte("requests per day"),
	[]byte("Generate requests per day"),
	[]byte("Max retries exceeded"),
	[]byte("Terminal error"),
}

const (
	retryBackoffMarker     = "Retrying with backoff"
	DefaultQuotaWindowSize = 8192
)

var (
	retryBackoffMarkerBytes = []byte(retryBackoffMarker)
	status429Marker         = []byte("status: 429")
)

// IsUnambiguousFatalQuotaError checks for fatal quota or key suspension errors
// that are unrecoverable regardless of whether the CLI attempts a backoff retry.
// Note: Generic 429 messages from generativelanguage.googleapis.com ("You exceeded your
// current quota, please check your plan and billing details") are returned on transient
// RPM/TPM rate limits as well, so they are classified as ambiguous and deferred when
// accompanied by "Retrying with backoff".
func IsUnambiguousFatalQuotaError(data []byte) bool {
	if IsSuspendedKeyError(data) {
		return true
	}

	for _, marker := range unambiguousFatalMarkers {
		if bytes.Contains(data, marker) {
			return true
		}
	}
	return false
}

// HasAmbiguousQuotaError checks for HTTP 429 or RESOURCE_EXHAUSTED indicators
// that may be either transient (if accompanied by "Retrying with backoff") or fatal.
func HasAmbiguousQuotaError(data []byte) bool {
	for _, marker := range ambiguousQuotaMarkers {
		if bytes.Contains(data, marker) {
			return true
		}
	}
	return false
}

// ContainsRetryBackoff checks if the output contains a backoff retry indicator.
func ContainsRetryBackoff(data []byte) bool {
	return bytes.Contains(data, retryBackoffMarkerBytes)
}

// IsFatalQuotaError checks if the output indicates daily quota exhaustion (RPD) or unrecoverable billing/quota errors,
// ignoring intermediate transient RPM/TPM retry messages ("Retrying with backoff").
func IsFatalQuotaError(data []byte) bool {
	if IsUnambiguousFatalQuotaError(data) {
		return true
	}

	// If the log indicates an active retry with backoff, treat as transient rate limit rather than fatal RPD quota.
	if ContainsRetryBackoff(data) {
		return false
	}

	return HasAmbiguousQuotaError(data)
}

// IsTransientRateLimit checks if the output indicates a transient RPM/TPM rate limit spike being retried.
func IsTransientRateLimit(data []byte) bool {
	if IsFatalQuotaError(data) {
		return false
	}
	return bytes.Contains(data, retryBackoffMarkerBytes) ||
		bytes.Contains(data, status429Marker)
}

func ContainsQuotaError(data []byte) bool {
	return IsFatalQuotaError(data)
}

type retryInterval struct {
	start int
	end   int
}

// QuotaStreamTracker buffers streamed log chunks across polling intervals to
// robustly detect fatal quota and key-suspension errors without false positives
// when retry messages ("Retrying with backoff") and error payloads ("RESOURCE_EXHAUSTED" / "429")
// are split across chunk boundaries or separate poll ticks.
type QuotaStreamTracker struct {
	window            []byte
	windowStartOffset int64
	totalOffset       int64
	maxWindowSize     int

	pendingAmbiguous       bool
	pendingAmbiguousOffset int64
}

// NewQuotaStreamTracker creates a QuotaStreamTracker with the default sliding window size.
func NewQuotaStreamTracker() *QuotaStreamTracker {
	return &QuotaStreamTracker{
		maxWindowSize: DefaultQuotaWindowSize,
	}
}

// Window returns the current rolling log buffer for extracting metadata such as API keys or models.
func (t *QuotaStreamTracker) Window() []byte {
	return t.window
}

// trimWindow drops the oldest bytes once the window exceeds maxWindowSize.
//
// The retained bytes are shifted to the front of the existing backing array rather than
// resliced with t.window[excess:]. Reslicing would advance the slice pointer and shrink
// the remaining capacity on every poll, so the window would walk through its backing array
// and force a fresh allocation + copy every time that capacity ran out. Shifting in place
// keeps the steady-state poll path free of allocations.
func (t *QuotaStreamTracker) trimWindow() {
	if len(t.window) > t.maxWindowSize {
		excess := len(t.window) - t.maxWindowSize
		copy(t.window, t.window[excess:])
		t.window = t.window[:t.maxWindowSize]
		t.windowStartOffset += int64(excess)
	}
}

// appendChunk folds a newly read log chunk into the rolling window and returns the
// buffer that should be analyzed along with the stream offset of its first byte.
//
// Retained memory is bounded *before* analysis rather than after it: a single poll can
// return an arbitrarily large chunk (e.g. reattaching to a long-running task tails the
// whole existing log from offset 0), and appending that to the window would transiently
// allocate len(window)+len(chunk) bytes. Oversized chunks are therefore scanned in place
// (no copy) and only their trailing maxWindowSize bytes are retained for subsequent polls.
func (t *QuotaStreamTracker) appendChunk(newData []byte) (analysisBuf []byte, baseOffset int64) {
	if len(newData) > t.maxWindowSize {
		chunkStart := t.totalOffset
		t.totalOffset += int64(len(newData))
		// Reuse the existing backing array; it is already capped at maxWindowSize.
		t.window = append(t.window[:0], newData[len(newData)-t.maxWindowSize:]...)
		t.windowStartOffset = t.totalOffset - int64(t.maxWindowSize)
		return newData, chunkStart
	}
	if len(newData) > 0 {
		t.window = append(t.window, newData...)
		t.totalOffset += int64(len(newData))
		t.trimWindow()
	}
	return t.window, t.windowStartOffset
}

// ObservePoll processes a delta chunk from a single poll interval while the task is running.
// Returns (isFatal, isTransient).
//   - Unambiguous fatal errors (e.g. billing RPD exhaustion, suspended keys, max retries exceeded)
//     trigger isFatal immediately.
//   - Ambiguous 429/RESOURCE_EXHAUSTED errors without a matching "Retrying with backoff" are held
//     for 1 poll grace period so that retry messages flushed slightly later or across chunk boundaries
//     are not falsely killed.
func (t *QuotaStreamTracker) ObservePoll(newData []byte) (bool, bool) {
	buf, baseOffset := t.appendChunk(newData)

	if IsUnambiguousFatalQuotaError(buf) {
		return true, false
	}

	found, earliestUncoveredOffset, hasTransient := findUncoveredAmbiguousError(buf, baseOffset)
	if !found {
		t.pendingAmbiguous = false
		isNewTransient := len(newData) > 0 && (ContainsRetryBackoff(newData) || (hasTransient && HasAmbiguousQuotaError(newData)))
		return false, isNewTransient
	}

	// If an uncovered error was already pending from a previous poll tick and remains uncovered,
	// the 1-poll grace period has expired without a retry message.
	if t.pendingAmbiguous && earliestUncoveredOffset <= t.pendingAmbiguousOffset {
		return true, false
	}

	// First poll seeing this uncovered ambiguous error: start the 1-poll grace period.
	t.pendingAmbiguous = true
	t.pendingAmbiguousOffset = earliestUncoveredOffset
	return false, false
}

// ObserveFinal processes any remaining log output when the task process exits.
// Unlike ObservePoll, it does not wait an additional poll interval.
// If processFailed is true (non-zero exit code or abnormal termination), any uncovered
// ambiguous quota error in the window is treated as fatal.
func (t *QuotaStreamTracker) ObserveFinal(finalData []byte, processFailed bool) bool {
	buf, baseOffset := t.appendChunk(finalData)

	if IsUnambiguousFatalQuotaError(buf) {
		return true
	}
	if !processFailed {
		return false
	}
	found, _, _ := findUncoveredAmbiguousError(buf, baseOffset)
	return found
}

// findUncoveredAmbiguousError scans buf (operating directly on the bytes, without
// materializing a string copy) for ambiguous quota indicators that are not covered by a
// nearby "Retrying with backoff" message. baseOffset is the stream offset of buf[0], so
// returned offsets remain stable as the rolling window slides.
func findUncoveredAmbiguousError(buf []byte, baseOffset int64) (found bool, earliestUncoveredOffset int64, hasTransient bool) {
	var intervals []retryInterval
	searchIdx := 0
	for {
		idx := bytes.Index(buf[searchIdx:], retryBackoffMarkerBytes)
		if idx == -1 {
			break
		}
		retryStart := searchIdx + idx
		coverStart := retryStart - 2048
		if coverStart < 0 {
			coverStart = 0
		}
		coverEnd := computeRetryCoverEnd(buf, retryStart)
		intervals = append(intervals, retryInterval{
			start: coverStart,
			end:   coverEnd,
		})
		hasTransient = true
		searchIdx = retryStart + len(retryBackoffMarkerBytes)
	}

	for _, marker := range ambiguousQuotaMarkers {
		searchIdx = 0
		for {
			idx := bytes.Index(buf[searchIdx:], marker)
			if idx == -1 {
				break
			}
			errStart := searchIdx + idx
			covered := false
			for _, iv := range intervals {
				if errStart >= iv.start && errStart <= iv.end {
					covered = true
					break
				}
			}
			if covered {
				hasTransient = true
			} else {
				streamOffset := baseOffset + int64(errStart)
				if !found || streamOffset < earliestUncoveredOffset {
					found = true
					earliestUncoveredOffset = streamOffset
				}
			}
			searchIdx = errStart + len(marker)
		}
	}

	return found, earliestUncoveredOffset, hasTransient
}

var (
	spacePrefix     = []byte(" ")
	tabPrefix       = []byte("\t")
	apiErrorPrefix  = []byte("_ApiError")
	openBracePrefix = []byte("{")
)

// isPayloadContinuation reports whether the line following a newline is a continuation of a
// multi-line error payload rather than the start of unrelated log output.
func isPayloadContinuation(line []byte) bool {
	return bytes.HasPrefix(line, spacePrefix) ||
		bytes.HasPrefix(line, tabPrefix) ||
		bytes.HasPrefix(line, apiErrorPrefix) ||
		bytes.HasPrefix(line, openBracePrefix)
}

// computeRetryCoverEnd returns the index at which the log region "covered" by the retry
// message starting at retryStart ends: the first newline that is outside any brace-delimited
// payload and is not followed by a continuation line, capped 2048 bytes past retryStart.
func computeRetryCoverEnd(buf []byte, retryStart int) int {
	maxEnd := retryStart + 2048
	if maxEnd > len(buf) {
		maxEnd = len(buf)
	}
	braceDepth := 0
	for i := retryStart; i < maxEnd; i++ {
		switch ch := buf[i]; {
		case ch == '{':
			braceDepth++
		case ch == '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case ch == '\n' && braceDepth == 0:
			if isPayloadContinuation(buf[i+1:]) {
				// Still inside a multi-line payload; keep scanning.
				continue
			}
			return i
		}
	}
	return maxEnd
}

func GetGeminiAPIKey(secret *corev1.Secret) string {
	if token, err := getTokenFromScript(); err == nil && token != "" {
		return token
	}
	if secret != nil {
		return string(secret.Data[constants.KeyGeminiAPIKey])
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

		if !IsKeyAllModelsQuotaExceeded(token) && !IsKeySuspended(token) {
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
	Total             int      `json:"total"`
	QuotaExceeded     int      `json:"quotaExceeded"`
	Suspended         int      `json:"suspended"`
	Active            int      `json:"active"`
	ActiveList        []string `json:"activeList"`
	QuotaExceededList []string `json:"quotaExceededList"`
	SuspendedList     []string `json:"suspendedList"`
}

func GetTokensStatus() (*TokensStatus, error) {
	allTokens, err := DiscoverTokensFromScript()
	if err != nil {
		return nil, err
	}

	status := &TokensStatus{
		ActiveList:        make([]string, 0),
		QuotaExceededList: make([]string, 0),
		SuspendedList:     make([]string, 0),
	}
	seenSuspended := make(map[string]bool)

	suspendedMap, _ := loadSuspendedList()
	for k := range suspendedMap {
		if k != "" {
			seenSuspended[k] = true
		}
	}

	// Load quota exceeded list once and prune expired entries to avoid disk I/O in the loop
	listMutex.Lock()
	quotaList, _ := loadQuotaExceededList()
	quotaModified := false
	now := time.Now()
	for k, exp := range quotaList {
		if now.After(exp) {
			delete(quotaList, k)
			quotaModified = true
		}
	}
	if quotaModified {
		_ = saveQuotaExceededList(quotaList)
	}
	listMutex.Unlock()

	// Keep track of all suspended keys uniquely
	suspendedKeysMap := make(map[string]bool)
	for k := range suspendedMap {
		if k != "" {
			suspendedKeysMap[k] = true
		}
	}

	for _, t := range allTokens {
		obscured := t
		if len(obscured) > 8 {
			obscured = obscured[:8] + "..."
		}
		if seenSuspended[t] {
			suspendedKeysMap[t] = true
		} else {
			// Check specific models dynamically from quotaList
			var exceededModels []string
			legacyExceeded := false

			// If legacy key is present, it's exceeded for everything
			if exp, exists := quotaList[t]; exists && !now.After(exp) {
				legacyExceeded = true
				exceededModels = append(exceededModels, DefaultModels...)
			} else {
				// Otherwise check individual models
				for _, m := range DefaultModels {
					mapKey := t + ":" + m
					if exp, exists := quotaList[mapKey]; exists && !now.After(exp) {
						exceededModels = append(exceededModels, m)
					}
				}
			}

			if len(exceededModels) == len(DefaultModels) {
				// Exceeded for ALL models -> fully Quota Exceeded
				if legacyExceeded {
					status.QuotaExceededList = append(status.QuotaExceededList, obscured)
				} else {
					sort.Strings(exceededModels)
					status.QuotaExceededList = append(status.QuotaExceededList, fmt.Sprintf("%s (%s)", obscured, strings.Join(exceededModels, ", ")))
				}
			} else if len(exceededModels) > 0 {
				// Exceeded for some but not all models -> Active (Degraded)
				sort.Strings(exceededModels)
				status.ActiveList = append(status.ActiveList, fmt.Sprintf("%s (Degraded: exceeded %s)", obscured, strings.Join(exceededModels, ", ")))
			} else {
				// Healthy
				status.ActiveList = append(status.ActiveList, obscured)
			}
		}
	}

	// Populate suspended list with obscured versions
	for k := range suspendedKeysMap {
		obscured := k
		if len(obscured) > 8 {
			obscured = obscured[:8] + "..."
		}
		status.SuspendedList = append(status.SuspendedList, obscured)
	}

	// Sort lists for perfect determinism
	sort.Strings(status.ActiveList)
	sort.Strings(status.QuotaExceededList)
	sort.Strings(status.SuspendedList)

	status.Suspended = len(status.SuspendedList)
	status.QuotaExceeded = len(status.QuotaExceededList)
	status.Active = len(status.ActiveList)
	status.Total = status.Suspended + status.QuotaExceeded + status.Active
	return status, nil
}
