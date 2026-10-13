package taskfiles

import (
	"fmt"
	"regexp"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/geminitokens"
	"k8s.io/klog/v2"
)

var modelRegexp = regexp.MustCompile(`(?i)Trying model:\s*([a-zA-Z0-9\-\._\/]+)`)

// QuotaWatch reads a task's log as it arrives for the quota and
// suspension errors that end it, recording the exceeded models and keys
// on the way, so that the next task does not pick them.
type QuotaWatch struct {
	tracker   *geminitokens.QuotaStreamTracker
	envs      map[string]string
	lastModel string
}

// NewQuotaWatch watches a task run with envs, whose API key is the one
// recorded when an error does not name its own.
func NewQuotaWatch(envs map[string]string) *QuotaWatch {
	return &QuotaWatch{tracker: geminitokens.NewQuotaStreamTracker(), envs: envs}
}

// Poll observes a chunk of the log read while the task runs. An error is
// a fatal quota or suspension error: the caller kills the task's process
// group and returns it.
func (q *QuotaWatch) Poll(chunk []byte) error {
	isFatal, isTransient := q.tracker.ObservePoll(chunk)
	evidence := quotaEvidence(chunk, q.tracker.Window())
	if model := recordExceededModels(evidence, q.tracker, q.envs); model != "" {
		q.lastModel = model
	}
	if isFatal {
		return handleQuotaOrSuspensionError(evidence, q.lastModel, q.envs)
	}
	if isTransient {
		klog.V(2).Infof("Transient rate limit (RPM/TPM) detected in task output; allowing CLI to retry with backoff...")
	}
	return nil
}

// Final observes the rest of the log once the task has ended, failed or
// not. An error is a quota or suspension error that ended it: the caller
// records the task as killed (137) and returns it.
func (q *QuotaWatch) Final(chunk []byte, failed bool) error {
	isFatal := q.tracker.ObserveFinal(chunk, failed)
	evidence := quotaEvidence(chunk, q.tracker.Window())
	if model := recordExceededModels(evidence, q.tracker, q.envs); model != "" {
		q.lastModel = model
	}
	if isFatal {
		return handleQuotaOrSuspensionError(evidence, q.lastModel, q.envs)
	}
	return nil
}

func recordExceededModels(evidence []byte, quotaTracker *geminitokens.QuotaStreamTracker, envs map[string]string) string {
	newlyExceeded := quotaTracker.NewlyExceededModels()

	key := geminitokens.ExtractAPIKeyFromError(evidence)
	if key == "" {
		key = envs[constants.KeyGeminiAPIKey]
	}

	if len(newlyExceeded) > 0 {
		if key != "" {
			for _, m := range newlyExceeded {
				klog.Warningf("Model '%s' failed after encountering quota error; marking as quota exceeded for key", m)
				if err := geminitokens.AddQuotaExceededKeyAndModel(key, m, 4*time.Hour); err != nil {
					klog.Errorf("Failed to mark key and model as quota exceeded: %v", err)
				}
			}
		} else {
			klog.Warningf("Cannot record quota exceeded models %v: API key could not be resolved from error or environment", newlyExceeded)
		}
		quotaTracker.AckExceededModels(newlyExceeded...)
		return newlyExceeded[len(newlyExceeded)-1]
	}

	if exceeded := quotaTracker.ExceededModels(); len(exceeded) > 0 {
		return exceeded[len(exceeded)-1]
	}

	if quotaTracker.CurrentModel() == "" {
		return extractModelFromLogs(evidence)
	}

	return ""
}

// quotaEvidence returns the buffer to use for post-mortem extraction of failure metadata
// (model name, API key, suspension vs. quota classification).
//
// The tracker window is capped at geminitokens.DefaultQuotaWindowSize, so the two buffers
// are not interchangeable: for an ordinary poll the window is a superset of the chunk (it
// also carries history from previous ticks), but for an oversized chunk the window holds
// only its trailing bytes and the chunk is the superset. Whichever is longer contains the
// other, so picking the longer one never loses information - notably a "Trying model: ..."
// line printed near the start of a large chunk.
func quotaEvidence(chunk []byte, window []byte) []byte {
	if len(chunk) > len(window) {
		return chunk
	}
	return window
}

func extractModelFromLogs(logs []byte) string {
	matches := modelRegexp.FindAllSubmatch(logs, -1)
	if len(matches) > 0 {
		lastMatch := matches[len(matches)-1]
		if len(lastMatch) > 1 {
			return string(lastMatch[1])
		}
	}
	return ""
}

func handleQuotaOrSuspensionError(newData []byte, lastModelTried string, envs map[string]string) error {
	key := geminitokens.ExtractAPIKeyFromError(newData)
	if key == "" {
		key = envs[constants.KeyGeminiAPIKey]
	}
	if key != "" {
		if geminitokens.IsSuspendedKeyError(newData) {
			if err := geminitokens.AddSuspendedKey(key); err != nil {
				klog.Errorf("Failed to mark key as suspended: %v", err)
			}
		} else {
			if err := geminitokens.AddQuotaExceededKeyAndModel(key, lastModelTried, 4*time.Hour); err != nil {
				klog.Errorf("Failed to mark key and model as quota exceeded: %v", err)
			}
		}
	}
	if geminitokens.IsSuspendedKeyError(newData) {
		return fmt.Errorf("task failed due to suspended Gemini API key (CONSUMER_SUSPENDED)")
	}
	return fmt.Errorf("task failed due to quota exceeded (RESOURCE_EXHAUSTED / 429)")
}
