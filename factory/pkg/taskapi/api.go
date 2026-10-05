// Package taskapi is how factory reaches a sandbox's tasks without envd:
// a small REST server in the sandbox's daemon, bound to the pod's
// loopback and reached through a port-forward, so that the API server
// authenticates and authorises every caller and nothing listens on the
// pod's IP.
//
//	GET  /v1/version                     {"api":1}
//	GET  /v1/tasks                       every task, newest first
//	POST /v1/tasks                       start a recipe task; idempotent by id and run name
//	GET  /v1/tasks/{id}                  one task
//	GET  /v1/tasks/{id}/log?offset=N     the log from byte N, following it until the task ends
//	GET  /v1/tasks/{id}/files            the task directory's files
//	GET  /v1/tasks/{id}/files/{name}     one of them
//	PUT  /v1/tasks/{id}/files/{name}     the applied marker, the only file a client writes
//	POST /v1/tasks/{id}/cancel           end the task's process group
//	     /v1/sessions[/...]              acpd's agent sessions, when the daemon hosts them
//
// The task directories stay what they are, so a task the server started
// and one envd started read alike, by either.
package taskapi

import "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"

const (
	// Port is the task server's, on the sandbox pod's loopback only.
	Port = 49990
	// APIVersion is what this server serves. A client needing more than a
	// sandbox's server reports falls back to envd.
	APIVersion = 1
)

// Version is what GET /v1/version answers.
type Version struct {
	API int `json:"api"`
	// Sessions is the version of acpd's session API served under
	// /v1/sessions, 0 when it is not: a separate number so that a client
	// of tasks alone keeps working with a server that has none.
	Sessions int `json:"sessions,omitempty"`
}

// PostRequest is a recipe task to start. The server builds the command
// from the recipe and inputs, as the spool does: it never runs one it is
// given. Env reaches the task's process and is never written to disk.
type PostRequest struct {
	Task   spool.Task        `json:"task"`
	Recipe []byte            `json:"recipe"`
	Inputs map[string]string `json:"inputs"`
	Env    map[string]string `json:"env,omitempty"`
}

// PostResponse is the task as started, or as found when Existed: a task
// of the same id or run name was posted before, and nothing was started.
type PostResponse struct {
	Task    spool.Entry `json:"task"`
	Existed bool        `json:"existed,omitempty"`
}

// CancelRequest ends a task: SIGTERM to its process group and exit code
// 143, or with Kill, SIGKILL and 137, as a quota kill records it.
type CancelRequest struct {
	Kill bool `json:"kill,omitempty"`
}

// errorBody is every error's body.
type errorBody struct {
	Error string `json:"error"`
}
