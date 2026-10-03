# Gemini for Kubernetes Development

Coding agents for real GitHub work, run on Kubernetes. Every task (an issue fix, a PR follow-up, a review, a deployment, a research conversation) gets its own sandbox in the cluster: a persistent workspace, its own credentials, and an agent (Gemini CLI, Claude Code or Antigravity) that keeps running when your laptop disconnects.

Running one agent on one issue is easy. Running dozens against a busy repository is not. Somebody has to give each task an isolated environment, answer CI failures and review comments, rebase on conflicts, know when to stop, hand finished work to a human, survive restarts, and clean up afterwards. This repository is that machinery, in three parts.

| | What it is | Use it when |
|---|---|---|
| **[factory](factory/)** | The engine. A CLI that runs one agent task in a Kubernetes sandbox: `fix`, `plan`, `recipe triage`, `pr review / investigate / address-comments / iterate / watch`, runbook deployments (`run plan / deploy / teardown`), research conversations, plus `factory watch`, the autonomous loop. | You want to run agent tasks yourself, from a terminal or a script. |
| **[overseer](overseer/)** | Unattended operation. An `Overseer` custom resource runs `factory watch` against one repository, in its own namespace with a bot account pool. It picks up labelled issues, opens PRs, works them until CI is green and comments are addressed, labels them ready for a human, runs scheduled chores, and garbage-collects sandboxes. | A repository should have agents working its backlog around the clock, steered from GitHub labels. |
| **[repo-agent](repo-agent/)** | The human in the loop. A multi-user web app (UI, API, controller) with a board per repository: what needs you next, one click to triage, plan, fix, review, deploy or research, and every GitHub write made as *you*, after you have seen it. | People want agents to do the legwork while staying the author of everything GitHub sees. |

```
            repo-agent (per-member boards, web UI)        overseer (one per repository, unattended)
                          │                                            │
                          │  runs the factory CLI                      │  runs factory watch
                          ▼                                            ▼
                  ┌──────────────────────────── factory ─────────────────────────────┐
                  │  one agent-sandbox Sandbox per task: /workspaces PVC, envd, acpd │
                  │  Gemini CLI · Claude Code · Antigravity                          │
                  └──────────────────────────────────────────────────────────────────┘
```

factory has no dependency on the other two. overseer and repo-agent both drive factory, and neither depends on the other: you can run overseer without repo-agent, and repo-agent without overseer (its Overseer page is an optional dashboard).

## Getting started

- **Try a task from your terminal**: build factory, run `factory up` against a kind cluster, then `factory fix --url <issue URL>`. See the [factory README](factory/README.md#getting-started).
- **Put a repository on autopilot**: install the controller and apply an `Overseer`. See the [overseer README](overseer/README.md#deploying) and the [user guide](overseer/docs/user-guide.md).
- **Give a team boards**: follow the repo-agent [Quick Start](repo-agent/docs/quick-start.md), or the [Development Guide](repo-agent/docs/development.md) to build from source.

## Repository layout

| Path | Contents |
|---|---|
| [`factory/`](factory/) | The factory CLI, the sandbox daemon (envd, acpd), task scripts and worker images. |
| [`overseer/`](overseer/) | The `Overseer` CRD and controller, the Overseer pod image and its watch loop. |
| [`repo-agent/`](repo-agent/) | The web UI, API, RepoBoard controller and their images. |
| [`docs/`](docs/) | Cross-cutting guides (GKE install, rotating bot secrets). |
| [`dev/`](dev/) | Build, CI and release tooling shared by the modules. |
| [`extras/`](extras/) | Smaller, independent projects that live in this repository: the Kubernetes Gemini CLI extension, devcontainer features, and more. |

## License

Apache 2.0. See [LICENSE](LICENSE).
