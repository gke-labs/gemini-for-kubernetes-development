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

// GetEnvInt parses an int from an environment variable with a fallback default.
func GetEnvInt(key string, defaultValue int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	var res int
	for _, c := range val {
		if c < '0' || c > '9' {
			klog.Warningf("Failed to parse %s=%q as int, using default %d", key, val, defaultValue)
			return defaultValue
		}
		res = res*10 + int(c-'0')
	}
	return res
}
