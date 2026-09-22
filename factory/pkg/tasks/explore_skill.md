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
- `runbooks/<scenario>.md` — an executable path through a scenario:
  `deploy.md`, `upgrade.md`, and kin. Every step is a command derived
  from the repo's own tooling (Makefile, scripts, CI workflows), never
  invented. Fixed sections, in order: **What this needs** (what
  running it requires and what permissions or credentials each step
  assumes), **Preconditions**, **Steps**, **Verify** (how you know it
  worked: endpoints to probe, commands whose output proves health),
  **Teardown**. A runbook a reader cannot execute top-to-bottom is a
  bug.

## Runbook requirements and targets

Every runbook opens its **What this needs** section by answering one
question plainly: **can this run in the pod, or does it need real
infrastructure?**

- **In the pod** — binaries, unit and integration tests, envtest
  (etcd + kube-apiserver as plain processes), single-process servers
  exposed on a port. No new permissions, nothing to clean up beyond
  the sandbox.
- **Real infrastructure** — a cluster or cloud: anything needing real
  nodes (CSI drivers, device plugins, kernel modules, privileged
  DaemonSets, kubelet plugin sockets, host mounts), real cloud APIs,
  VMs. Say exactly which component forces it, what credentials each
  step assumes, and what it costs to tear down.

When the answer is "both prove something" — tests in the pod, the
real thing on a cluster — say so and write a path for each.

When more than one deployment target genuinely proves something,
write a path per target, named by the target — the thing a user
actually deploys to: Steps carries one subsection per path
("### Path — in-pod: …", "### Path — GKE: …"), in-pod first when it exists, and What this needs says what each path demands.
Only targets that prove something real get a path; never pad one with
invented steps. If a path outgrows the file (rule 6), split it into
`runbooks/<scenario>-<target>.md` (`deploy-gke.md`, `deploy-kind.md`,
`deploy-kops.md`) and link it from the main runbook.

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
