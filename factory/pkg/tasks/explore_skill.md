---
name: exploration
description: Maintain a personal understanding of this repository in docs-exploration/ — what it does, how it is organized, how it compares to alternatives, and what changed recently. Use when exploring the codebase, answering architecture questions, or updating onboarding notes.
---

# Repository exploration notes

You maintain a living set of understanding documents under
`docs-exploration/` on the `exploration/notes` branch. They exist so a
new or returning contributor catches up fast. Keep them truthful,
current, and short enough to read.

## The tree

- `README.md` — the index. One line per document, freshest first. Always
  update it when any other file changes.
- `overview.md` — what this project does, for whom, and why it exists.
  One page maximum.
- `architecture.md` — the components and how data flows between them.
  Use Mermaid diagrams (GitHub renders them). Name the key abstractions
  and where each lives.
- `code-map.md` — directory-by-directory: what lives where, the entry
  points, and the ~20 files that matter most. Mark files that are
  dangerous to modify and say why.
- `comparisons/<alternative>.md` — how this project differs from a named
  alternative: philosophy, architecture, trade-offs. One file per
  alternative.
- `activity/<date>.md` — what happened in a recent window: themes, not
  commit lists. Where the churn is, what merged that matters, what the
  maintainers are asking for help with.
- `sessions/<date>-<topic>.md` — distilled notes from interactive
  question-and-answer sessions. Write the answer that would have saved
  the questioner an hour, not a transcript.
- `runbooks/<scenario>-<environment>.md` — one runbook per scenario
  per environment: `deploy-gcp.md`, `deploy-in-pod.md`,
  `upgrade-gcp.md`. Each is executable top-to-bottom on its own —
  no shared context, no cross-references required. Every step is a
  command derived from the repo's own tooling (Makefile, scripts, CI
  workflows), never invented. Fixed sections, in order: **What this
  needs** (what running it requires and what permissions or
  credentials each step assumes), **Preconditions**, **Steps**,
  **Verify** (how you know it worked: endpoints to probe, commands
  whose output proves health), **Teardown**. A runbook a reader
  cannot execute top-to-bottom is a bug.

## Runbook environments

The environment is in the filename, and the decision is binary:

- **`-in-pod`** — runs where the agent runs: binaries, unit and
  integration tests, envtest (etcd + kube-apiserver as plain
  processes), single-process servers exposed on a port. No new
  permissions, nothing to clean up beyond the sandbox.
- **`-gcp`** — needs real infrastructure: real nodes (CSI drivers,
  device plugins, kernel modules, privileged DaemonSets, kubelet
  plugin sockets, host mounts), real cloud APIs, VMs. The name says
  where the credentials point, not which product — What this needs
  states the services actually used (a GKE cluster, GCE VMs, Cloud
  Run, …), exactly which component forces real infrastructure, and
  what teardown costs. It also carries a VERIFIED feasibility
  checklist: permissions and tools probed read-only under the
  executing identity at drafting time, each item ✓ or ✗ MISSING with
  the exact command that fixes it. A runbook whose checklist has an
  ✗ is a request to the owner, not a candidate for a Run.

Deployment instances OWN what they create: runbook steps name every
cloud resource `${RESOURCE_PREFIX}[-suffix]` (a project-unique prefix
the harness provides) and never adopt infrastructure the instance
did not create — an existing cluster belongs to another instance
unless the owner explicitly names it in guidance.

Write only the runbooks that prove something: a library with no
in-pod story gets no `-in-pod` file; never pad one with invented
steps. Finer-grained environment names (`-gke`, `-gce`, `-kind`) are
for repos that genuinely offer alternatives worth separate runbooks.

The drafted set is a starting convention, not a taxonomy: the owner
creates custom runbooks by name from a free-form description
(`deploy-kops-gce`, `deploy-gke-autopilot`, `perf-test-gce`), and
they are first-class peers of the drafted ones — same fixed sections,
same derive-from-tooling rule, same pins, same instances. The name is
a slug; the content defines what it does.

- `questions.md` — open questions. Add what you could not resolve;
  remove what later work answers.

## Rules

1. **Edit in place.** These are living documents, not appended logs.
   When understanding improves, rewrite the relevant section.
2. **Derive from code, not from docs.** READMEs and comments drift; the
   source is the record. When docs and code disagree, say so in
   `questions.md`.
3. **No inventories.** Never list every dependency, every file, or every
   function. Name what a contributor must know, link the rest.
4. **Diagrams over prose** for structure. Mermaid `graph` or
   `sequenceDiagram`, kept small enough to render legibly.
5. **Date your claims** in `activity/` and `sessions/` files; undated
   understanding rots silently.
6. **Stay under ~150 lines per document.** Split before you sprawl.
7. After substantial interactive answers, distill into
   `sessions/<date>-<topic>.md` and fold durable insights into the main
   documents.
8. **Runbooks state their requirements before their steps.** The "What
   this needs" section is what a user reads to decide whether to run
   it — keep it honest and specific, including what it costs to tear
   down.
9. **Human edits are decisions, not drift.** This branch belongs to
   its owner and edits to it are the review channel. Any line marked
   `(pinned)` — a target choice, a parameter, a requirement — is a
   constraint: never change it back; rewrite the steps to fit it, and
   if the code suggests otherwise, note your disagreement in one line
   directly under it instead of reverting. The same respect applies
   to any section a person has clearly rewritten.
