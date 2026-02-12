# Overseer Design: The Autonomous Agentic Loop

The Overseer (also referred to as Principal, Arbiter, or Oracle) is an autonomous agent responsible for orchestrating other agents and managing the state of the repository.

Unlike traditional controllers with rigid logic, the Overseer operates as an LLM-driven agent in a continuous loop, observing the system, understanding the desired state, and taking actions to move the system towards that state.

## 1. Core Philosophy

The Overseer is built on the following principles:

1.  **Observation**: Continuously observe the system (the GitHub repository) and respond to events.
2.  **Intent Understanding**: Grok the desired state of the system from user intent (issues, PRDs, design docs) and current state.
3.  **Dynamic Goals**: The desired state is a moving target determined by user intent and the evolving system state.
4.  **Orchestration**: Orchestrate other specialized agents to perform specific tasks (coding, reviewing, etc.) to achieve sub-goals.

## 2. The System Model

For the current iteration, the "System" is defined as a GitHub repository. The state of this system includes:

*   **Source Code**: The git repository itself.
*   **System Model**: The metadata exposed via the GitHub API (Issues, PRs, Comments, Workflows).
*   **Project Management**: Milestones, Project plans, etc.

## 3. Events

The Overseer monitors various events within the system, including but not limited to:

*   **GitHub Events**: Push events, Pull Request events, Issue events.
*   **Interactions**: Comments on PRs and Issues, reviews, workflow run statuses.
*   **External Interfaces**: Inputs from `gh` CLI, direct API calls, etc.

## 4. Desired State & Goals

The desired state is defined within the system itself (in the repo). This allows agents to define intermediate states/goals and update the system to reflect progress.

### Larger Goals
*   **High-level Issues**: Describing desired changes, features, or bug fixes.
*   **Documentation**: PRDs and Design Docs describing user intent.

### Sub-goals (Nudging the System)
*   **Issues & Sub-issues**: Created by the Overseer or other agents to break down larger goals. Specialized agents pick these up to perform work (create PR, update doc, release).
*   **Pull Requests**: Must be nudged towards "merge ready" status by:
    *   Addressing review comments.
    *   Fixing workflow failures.
    *   Rebasing when stale.

## 5. Overseer Architecture

The Overseer is designed as an agentic loop, distinct from a standard Kubernetes controller.

### Key Characteristics
1.  **LLM-Based**: It is an agent itself (e.g., `gemini-cli` instance) driven by a System Prompt.
2.  **Minimal Wrapper**: It has a minimal Go-code wrapper, primarily to invoke the agent loop.
3.  **Tool Usage**: It relies on external tools to observe and act:
    *   `gh` CLI
    *   GitHub API
    *   `git` CLI
4.  **Delegation**: It relies on sub-agents to do the heavy lifting. It breaks down paths to desired states into issues/sub-issues and triggers sub-agents to act on them.

### Actions
The Overseer's actions include:
1.  **Spinning up Sandboxes**: Launching environments for other agents to run.
2.  **State Updates**: Communicating by updating the state of the repository (creating issues, commenting on PRs).

## 6. Agents Ecosystem

The system uses `gemini-cli` as the core agent loop but is extensible to other agents (e.g., `claude-code`, `codex`).

### Agent Definitions
*   **Built-in Agents**: Standard agents for common tasks (Fixing issues, Reviewing PRs, Addressing comments).
*   **Custom Agents**: Defined in the repository under `.agent/*.md`.

### Agent Orchestration
The Overseer acts as the manager. When it detects a need (e.g., a new issue labeled `bug`), it:
1.  Analyzes the requirement.
2.  Identifies the appropriate agent (built-in or custom).
3.  Triggers the agent (e.g., by creating a Sandbox with the agent's context).

## 7. Implementation Plan

The implementation will focus on:
1.  **System Prompt**: Developing a robust system prompt for the Overseer that can interpret repository state and decide on orchestration actions.
2.  **Tool Integration**: Ensuring the Overseer has access to `gh`, `git`, and Kubernetes client tools (for sandbox creation).
3.  **Loop Mechanism**: A simple loop that periodically invokes the Overseer agent with the current context.
