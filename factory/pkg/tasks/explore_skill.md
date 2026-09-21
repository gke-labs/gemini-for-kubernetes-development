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
  invented. Fixed sections, in order: **What this needs** (opens with
  the Tier line, then what permissions or credentials each step
  assumes), **Preconditions**, **Steps**, **Verify** (how you know it
  worked: endpoints to probe, commands whose output proves health),
  **Teardown**. A runbook a reader cannot execute top-to-bottom is a
  bug.

## Runbook tiers

Every runbook opens its **What this needs** section with a tier call:

    **Tier**: <0|1|2> — <one line on why this tier and not a lower one>

- **Tier 0** — runs inside a plain container: binaries, unit and
  integration tests, envtest (etcd + kube-apiserver as processes). No
  new permissions.
- **Tier 1** — needs a real Kubernetes API, but a disposable,
  namespace-contained one (vcluster) suffices: controllers, operators,
  CRDs, webhooks — anything that talks only to the API server.
- **Tier 2** — needs real infrastructure: node-level features (CSI
  drivers, device plugins, kernel modules, privileged DaemonSets,
  kubelet plugin sockets, host mounts), real cloud APIs, VMs, or a
  full cluster (kind/GKE/kops). vcluster shares the host's nodes and
  kubelet, so anything that touches the node itself cannot land there.

Make the call carefully and say why: a controller that merely *ships*
a DaemonSet may still be tier 1 to exercise its reconcile logic, while
actually mounting a volume through it is tier 2.

When more than one tier genuinely proves something, write a path for
each viable tier — the Tier line lists them ("**Tier**: 1 (control
plane) / 2 (data path)") and Steps carries one subsection per path
("### Path — Tier 1: …"), lowest tier first. Only tiers that prove
something real get a path; never pad a tier with invented steps. If
one path outgrows the file (rule 6), split it into
`runbooks/<scenario>-tier<N>.md` and link it from the main runbook's
Tier line.
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
   its owner and edits to it are the review channel. A Tier line
   marked `(pinned)` — e.g. `**Tier**: 2 (pinned) — …` — is a
   constraint: never change it back; rewrite the steps to fit it, and
   if the code suggests otherwise, note your disagreement in one line
   directly under the Tier line instead of reverting it. The same
   respect applies to any section a person has clearly rewritten.
