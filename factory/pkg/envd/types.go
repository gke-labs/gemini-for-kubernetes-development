package envd

// Task represents an in-memory task posted directly to the sandbox pod via HTTP.
type Task struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
	State  string            `json:"taskState"` // Pending, Running, Completed, Failed
	Result string            `json:"result,omitempty"`
}
