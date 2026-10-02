# Repo Agent

Repo Agent is a work queue for GitHub repositories, with coding agents doing
the legwork. It runs on Kubernetes: a web UI, an API and a controller. The
actual agent work runs in sandboxes managed by the
[`factory`](../factory) CLI.

## The problem it solves

Maintainers and contributors spend their time answering one question: *what
do I do next?* The answer is scattered across issues waiting for triage, PRs
waiting for review, review comments and red CI on your own PRs, and
deployments you need to try out. Agents can do most of that preparation. But
many repositories do not accept bots as authors, and you shouldn't have to
let an agent post under your name without seeing what it wrote first.

Repo Agent puts it all in one table per repository and keeps a human in
charge of everything GitHub sees. Work is **discovered** live from GitHub and
from running sandboxes, never declared up front. Every action is a click
(or an opt-in automation scoped by you). Agents prepare drafts: triage
suggestions, plans, draft PRs, pending reviews. **Every GitHub write is made
with the identity and token of the person who started it**, after they have
seen it. Every member brings their own GitHub quota, so rate limits scale
with the team.

## Features

### Boards

- A **board** is a `RepoBoard` custom resource (`board.gemini.google.com`)
  scoped to one repository and owned by one member. Create one by pasting a
  repository URL on the Work page.
- The **Work** page groups rows into **Up Next** (everything waiting on you),
  **Review** (incoming PRs), **Issues** and **My PRs**. Each row shows what
  stage it is in, what the agent is doing (with the engine's icon), and one
  action button.
- An **All boards** view gives you a single inbox of needs-you rows across
  every board, plus Research and Runs views that span every board.
- **Board settings** (stored on the CR):
  - a view-only label filter;
  - opt-in automation (`auto.triage`, `auto.fix`, `auto.review`), limited by
    include/exclude labels and a recency window;
  - "ready to merge" rules (approvals, required checks);
  - draft-PR and auto-follow-up policies;
  - a concurrency limit (`limits.maxActive`);
  - sandbox image, disk size, idle-pause timeout and engine.
- **Requests**: every click becomes its own `Request` object
  (`board.gemini.google.com`). It moves through `Pending → Launching →
  Running → Succeeded | Failed`, so a failed launch is reported back instead
  of silently dropped.

### PR review and follow-ups

- **Review** runs a review as you and saves it on GitHub as a *pending*
  review that only you can see. You then finalize or abandon it from the
  board. **Review again** starts a fresh review.
- On your own PRs, **Agent ▾** sends the agent back to:
  - **address review comments**;
  - **fix failing CI** (investigate);
  - **iterate** with a free-form instruction (if you leave it empty, it
    resolves conflicts and iterates).
- **Auto follow-up** can be switched on or off for each PR, overriding the
  board default.
- **Promote PR** marks a draft PR as ready for review.

### Issues: triage, plans, fixes

- **Triage**: the agent drafts suggestions on the board. You edit them, then
  publish or reject. Nothing is written to GitHub before you publish.
- **Plan**: the agent drafts an implementation plan. You can refine it with
  feedback, continue the planning conversation in a terminal, approve it
  (which starts the fix) or reject it.
- **Fix**: assigns the issue to you and runs a fix in a sandbox. The result
  is a draft PR authored by you. **Fix** on the same row runs it again in the
  same sandbox.

### Runbooks (Runs tab)

- Each run follows a runbook procedure: **plan → deploy → run →
  teardown**. You review the plan (`runbook.md`) before you deploy.
- You can start a run from one of the repository's runbooks or copy an
  existing run's runbook.
- **Deploy ▾** on a PR row plans a run of that pull request (`--target
  <PR>`). Run chips on the row show the PR's deployments.
- Teardown verifies the resources are gone and writes a receipt. The
  all-boards Runs view works as a fleet dashboard for every deployment.

### Research conversations

- Ask an agent that has the repository checked out about it. You can start
  from a canned opening (repo overview, a digest of recent changes over a
  time window, a topic you name) or from an empty conversation.
- Each conversation runs in its own sandbox and talks to the agent over ACP
  through `acpd`. The API proxies it, and the UI streams events over a
  websocket that can resume where it left off. The transcript is stored on
  the sandbox, so reloading the page loses nothing.
- The conversation looks like a terminal, with two views (**rendered** and
  **raw**). Markdown tables are rendered, and ```` ```mermaid ```` blocks are
  drawn as diagrams. If a diagram fails to draw, you see its source instead
  of an error.
- When the agent needs permission for a tool, it asks inline. You can also
  change the approval mode, stop a turn, rename or delete the conversation,
  and open it in its own window (`#/research/<session>`).
- The board rail shows each conversation's live state (busy / needs you).
- **Save notes** (💾) writes the conversation as `<session name>.md` to the
  repository's `research/notes` branch.

### Engines

- Each board picks the engine for its tasks: **gemini** (the default),
  **claude** or **antigravity**. Changing it affects the next launch only;
  tasks already running finish on the engine they started with.
- Research conversations use the board's engine too.
- Claude needs an Anthropic API key in your namespace. Antigravity uses your
  Gemini API key.

### Multi-tenancy and credentials

- Users sign in with GitHub OAuth and must be on an allow-list. Each member
  gets their own namespace, named after their GitHub login. Their boards,
  requests, sandboxes and credentials live there.
- Members manage their own credentials on the **Settings** page: GitHub PAT,
  Gemini and Anthropic API keys, and an optional GCP project for deployments.
  Credentials can also be references to GCP Secret Manager. The controller
  keeps each member's sandbox credentials in their own namespace. Work for a
  member only ever runs with that member's credentials.
- Admins can switch to another member's namespace.

### Operations

- **Overseer** page (admins only): shows overseer instances, their chores
  (pause/resume, logs), sandboxes and tasks (logs, telemetry,
  pause/unpause, delete), and the incoming task queue (with per-task
  priority changes). It also has a **Factory / API Token Status** panel.
- **Usage** page: token usage from the factory token-usage collector.
- **Sandbox card**: a sandbox's tasks, logs and lifecycle, plus a browser
  terminal attached to the sandbox (see
  [terminal guide](docs/user-guide/terminal.md)).
- **Feedback**: file feedback from the UI, with an optional screenshot.

## Architecture

| Component | What it does |
| --- | --- |
| `review-ui` (`pr-review-ui`) | React single-page app: Work, Research, Runs, Overseer, Usage, Settings. |
| `review-api` (`pr-review-api`) | Gin API server (`pkg/api`). Handles auth and sessions, the board feed, creating `Request` objects, proxying research to `acpd`, proxying sandboxes and terminals, and proxying overseer and usage data. |
| `repowatch-controller` | Runs the RepoBoard reconciler (`pkg/controllers/repoboard`). It turns `Request`s and automation into factory invocations, and syncs member credentials. The image bundles the `factory` CLI and `kubectl`. |
| `syncer` | Copies selected resources (by default factory `Sandbox` objects) to a GCS bucket ([syncer setup](docs/design/syncer.md)). |
| `registry` | In-cluster image registry for development clusters. |

**How it uses factory:** repo-agent never imports factory code. The
controller runs the bundled `factory` CLI as child processes
(`pkg/factorycli`), for example `factory fix`, `factory pr review`,
`factory pr watch`, `factory run` and the research commands. It reads back
the labels and annotations factory puts on its sandboxes. factory and
overseer do not depend on repo-agent; repo-agent is the per-member UI and
controller layer on top of factory.

The ConfigDir CRDs and the `configdir-controller`/`configdir-cli` sources
are still in the tree, but the board flow does not use them.

## Getting started

### Prerequisites

| Tool | Why |
| --- | --- |
| [Gemini API key](https://aistudio.google.com) | Default agent engine (also used by antigravity). |
| [GitHub OAuth app](https://github.com/settings/developers) and/or [personal access token](https://github.com/settings/tokens) | Signing in, and acting on GitHub as yourself. |
| [Anthropic API key](https://console.anthropic.com) (optional) | Only for boards using the claude engine. |
| [KinD](https://kind.sigs.k8s.io/) or a GKE cluster | Somewhere to run it. |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | Talking to the cluster. |
| [Helm](https://helm.sh/docs/intro/install/) | Installing dependencies. |
| Go 1.26+, Docker, Node.js 18+ | Only when building from source. |

### Install

- **Latest release:** follow the [Quick Start](docs/quick-start.md).
- **From source:** follow the [Development Guide](docs/development.md)
  (`make` builds the images, creates a KinD cluster and deploys).
- **Iterating on a live cluster without merging:** see
  [Dev loop](docs/dev-loop.md).
- **Environment variables:** see [env-variables.md](docs/env-variables.md).

Then open the UI, add your credentials under **Settings**, and paste a
repository URL on the Work page to create your first board (or apply
[`examples/repoboard.yaml`](examples/repoboard.yaml)).

## Further reading

- [Usage guide](docs/user-guide/usage.md): boards, fixes and reviews.
- [RepoBoard design](docs/design/repoboard.md): the identity model, board
  spec and UI.
- [Factory CLI migration](docs/design/factory-cli-migration.md): how
  repo-agent moved its execution onto factory.
- [Interactive terminal](docs/user-guide/terminal.md) and
  [co-authoring](docs/user-guide/co-authoring.md).
- [Multi-tenant architecture](docs/design/tenancy.md): sessions, namespaces
  and isolation. Parts of it still describe the retired RepoWatch
  resources.
