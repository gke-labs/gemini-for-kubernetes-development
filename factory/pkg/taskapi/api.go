// Package taskapi is how factory reaches a sandbox's tasks: a small REST
// server in the sandbox's daemon, bound to the pod's
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
// The task directories are plain files (taskfiles.TaskFiles), which the
// daemon, the server and the spool all read alike.
package taskapi

import "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"

const (
	// Port is the task server's, on the sandbox pod's loopback only.
	Port = 49990
	// APIVersion is what this server serves. A sandbox whose server
	// reports less than a client needs is recreated.
	APIVersion = 1
)

// Version is what GET /v1/version answers.
type Version struct {
	API int `json:"api"`
	// Sessions is the version of acpd's session API served under
	// /v1/sessions, 0 when it is not: a separate number so that a client
	// of tasks alone keeps working with a server that has none.
	Sessions int `json:"sessions,omitempty"`
	// Secrets is the version of PostRequest.Secrets this server honours, 0
	// when it does not: a client must not send secrets to a server that
	// would put them in the task's environment, or leave them on disk.
	Secrets int `json:"secrets,omitempty"`
	// Uploads is the version of uploads this server serves (UploadsVersion),
	// 0 when it does not.
	Uploads int `json:"uploads,omitempty"`
}

// SecretsVersion is the PostRequest.Secrets this server honours.
const SecretsVersion = 1

// PostRequest is a recipe task to start. The server builds the command
// from the recipe and inputs, as the spool does: it never runs one it is
// given. Env reaches the task's process and is never written to disk.
type PostRequest struct {
	Task   spool.Task `json:"task"`
	Recipe []byte     `json:"recipe"`
	// Source is the recipe as the caller loaded it, kept beside it as
	// spool.RecipeSourceFile.
	Source []byte            `json:"source,omitempty"`
	Inputs map[string]string `json:"inputs"`
	Env    map[string]string `json:"env,omitempty"`
	// Secrets reach only the recipe steps allowed them, never the task's
	// environment, where the agent could read them back from /proc:
	// written to the task directory as spool.SecretsFile, mode 0600, for
	// the runner to read and delete as it starts, and never served.
	Secrets map[string]string `json:"secrets,omitempty"`
	// Upload names files uploaded before the task (PUT /v1/uploads/{name})
	// for the server to move into the task's directory at UploadTo, before
	// the task starts.
	Upload   string `json:"upload,omitempty"`
	UploadTo string `json:"uploadTo,omitempty"`
}

// Files are files the caller gives a task: Dir's, at To in the task's
// directory before its first step.
type Files struct {
	Dir string
	To  string
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
