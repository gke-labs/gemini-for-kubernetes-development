package common

import (
	"os"
	"time"

	"k8s.io/klog/v2"
)

// GetEnvDuration parses a time.Duration from an environment variable with a fallback default.
func GetEnvDuration(key string, defaultValue time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		klog.Warningf("Failed to parse %s=%q as duration: %v, using default %v", key, val, err, defaultValue)
		return defaultValue
	}
	return d
}
