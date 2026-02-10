# Overseer Design: Agentic Approach

This document outlines the design for the "Overseer" component in `repo-agent`.
The Overseer is an autonomous agent responsible for orchestrating other agents and managing repository events.

## Core Philosophy: Agentic Loop

Instead of rigid Go code defining the logic, the Overseer operates as an LLM-driven agent in a loop.
It uses a System Prompt and a set of Tools (MCP) to observe the state of the repository and take actions.

## Architecture

*   **Runtime**: The Overseer runs as a persistent process (e.g., in a Sandbox or as a Deployment).
*   **Logic**:
    *   **Observation**: The agent uses CLI tools (like `gh`) and GitHub APIs to poll for events, workflow runs, and status updates.
        *   Example: `gh api /orgs/gke-labs/events | jq ".[] | .repo.name , .type"`
        *   Example: `gh run list --json`
    *   **Decision**: Based on the System Prompt and observations, the agent decides what action to take.
    *   **Action**: The agent executes actions via Tools.

### Available Actions (Tools)

The Overseer can perform the following actions:

1.  **Comment**: Post comments on PRs or Issues.
2.  **Issue Management**: Create or update issues.
3.  **Sandbox Creation**:
    *   Create a sandbox for generating a PR (to fix an issue).
    *   Create a sandbox for reviewing a PR.
    *   Create a sandbox for running a specific task.
4.  **Orchestration**: Trigger other specialized agents defined in `.agent/`.

## Implementation Details

### System Prompt

The System Prompt will define the Overseer's role:
*   Monitor the repository for activity.
*   Triage incoming issues and PRs.
*   Delegate work to specialized agents (by creating Sandboxes for them).
*   Ensure that the "Human in the Loop" is kept informed but not overwhelmed.

### Tools & MCP

The Overseer will rely on the Model Context Protocol (MCP) or similar tool-use interfaces to interact with:
*   GitHub (via `gh` CLI or API).
*   Kubernetes (to create Sandboxes).
*   Local Filesystem (to read `.agent/` definitions).

## Why this approach?

*   **Flexibility**: Logic is defined by the prompt, not compiled code. Easy to adjust behavior.
*   **Extensibility**: Adding new capabilities is as simple as giving the agent a new tool.
*   **Intelligence**: The agent can make fuzzy decisions (e.g., "Is this issue distinct enough?") that are hard to code in Go.

