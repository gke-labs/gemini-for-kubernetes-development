// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"log"

	"github.com/gke-labs/gemini-for-kubernetes-development/agentsandboxes"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "agentsandboxes-mcp",
		Version: "0.1.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_sandboxes",
		Description: "List all agent sandboxes",
	}, listSandboxesHandler)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_sandbox",
		Description: "Create a new agent sandbox",
	}, createSandboxHandler)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

type emptyInput struct{}

func listSandboxesHandler(ctx context.Context, req *mcp.CallToolRequest, args emptyInput) (*mcp.CallToolResult, any, error) {
	client, err := agentsandboxes.NewClient()
	if err != nil {
		return nil, nil, err
	}

	sandboxes, err := client.List(ctx)
	if err != nil {
		return nil, nil, err
	}

	var results []string
	for _, s := range sandboxes {
		results = append(results, fmt.Sprintf("%s/%s", s.Namespace, s.Name))
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("Sandboxes: %v", results)},
		},
	}, sandboxes, nil
}

type createSandboxInput struct {
	Name  string `json:"name" jsonschema:"The name of the sandbox"`
	Image string `json:"image,omitempty" jsonschema:"The container image for the sandbox"`
}

func createSandboxHandler(ctx context.Context, req *mcp.CallToolRequest, args createSandboxInput) (*mcp.CallToolResult, any, error) {
	client, err := agentsandboxes.NewClient()
	if err != nil {
		return nil, nil, err
	}

	builder := client.New(args.Name)
	if args.Image != "" {
		builder.Image(args.Image)
	}

	sandbox, err := builder.Create(ctx)
	if err != nil {
		return nil, nil, err
	}

	msg := fmt.Sprintf("Created sandbox: %s/%s", sandbox.Namespace, sandbox.Name)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: msg},
		},
	}, sandbox, nil
}
