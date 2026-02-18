/*
Copyright 2026 The Gemini Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/agentsandboxes"
)

// This is a stub for an MCP server that manages agent sandboxes.
// It currently implements a very basic JSON-RPC loop over stdio.

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req JSONRPCRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			sendError(nil, -32700, "Parse error")
			continue
		}

		handleRequest(req)
	}
}

func handleRequest(req JSONRPCRequest) {
	ctx := context.Background()

	switch req.Method {
	case "list_sandboxes":
		sandboxes, err := agentsandboxes.List(ctx)
		if err != nil {
			sendError(req.ID, -32603, err.Error())
			return
		}
		sendResponse(req.ID, sandboxes)

	case "create_sandbox":
		var params struct {
			Name  string `json:"name"`
			Image string `json:"image"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			sendError(req.ID, -32602, "Invalid params")
			return
		}

		builder, err := agentsandboxes.New(params.Name)
		if err != nil {
			sendError(req.ID, -32603, err.Error())
			return
		}
		if params.Image != "" {
			builder.Image(params.Image)
		}

		sandbox, err := builder.Create(ctx)
		if err != nil {
			sendError(req.ID, -32603, err.Error())
			return
		}
		sendResponse(req.ID, sandbox)

	case "initialize":
		// MCP initialize handshake
		sendResponse(req.ID, map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"serverInfo": map[string]string{
				"name":    "agentsandboxes-mcp",
				"version": "0.1.0",
			},
		})

	default:
		sendError(req.ID, -32601, "Method not found")
	}
}

func sendResponse(id interface{}, result interface{}) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	send(resp)
}

func sendError(id interface{}, code int, message string) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	send(resp)
}

func send(v interface{}) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}
