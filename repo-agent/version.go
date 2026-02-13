package repoagent

// These constants are parsed by build tooling - be careful about changing the formats
const (
	//nolint:revive // var-naming
	REPOAGENT_RELEASE_VERSION = "v0.1.0-rc.3"
)

var (
	// GitCommit is the git commit hash of the binary.
	// It is set by the build tooling.
	GitCommit = "unknown"
)
