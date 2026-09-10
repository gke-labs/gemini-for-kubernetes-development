package concurrency

import (
	"sync"
)

// SandboxLockRegistry tracks in-flight leases on sandbox names to prevent
// multiple concurrent tasks from targeting the same sandbox.
type SandboxLockRegistry struct {
	mu     sync.Mutex
	leases map[string]string // sandboxName -> taskFilename
}

// NewSandboxLockRegistry creates a new SandboxLockRegistry instance.
func NewSandboxLockRegistry() *SandboxLockRegistry {
	return &SandboxLockRegistry{
		leases: make(map[string]string),
	}
}

// TryAcquire attempts to lease the given sandboxName for taskFilename.
// Returns true if the lease was acquired, or false if the sandbox is already busy.
// An empty sandboxName is always considered acquirable without recording a lease.
func (r *SandboxLockRegistry) TryAcquire(sandboxName, taskFilename string) bool {
	if r == nil || sandboxName == "" {
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, occupied := r.leases[sandboxName]; occupied {
		return false
	}
	r.leases[sandboxName] = taskFilename
	return true
}

// Release performs a scoped release that only clears the lease if held by taskFilename,
// preventing lease hijacking if another worker exits. If taskFilename is empty,
// the lease is unconditionally released.
// Returns true if the lease was released, or false if the lease was not held
// or was held by another task.
func (r *SandboxLockRegistry) Release(sandboxName, taskFilename string) bool {
	if r == nil || sandboxName == "" {
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	current, occupied := r.leases[sandboxName]
	if !occupied {
		return false
	}

	if taskFilename != "" && current != taskFilename {
		return false
	}

	delete(r.leases, sandboxName)
	return true
}

// IsBusy returns true if the given sandboxName is currently leased.
func (r *SandboxLockRegistry) IsBusy(sandboxName string) bool {
	if r == nil || sandboxName == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, busy := r.leases[sandboxName]
	return busy
}
