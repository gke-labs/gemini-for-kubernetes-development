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
  invented. Fixed sections, in order: **What this needs** (binary /
  container build / Kubernetes API / cloud APIs — and what permissions
  or credentials each step assumes), **Preconditions**, **Steps**,
  **Verify** (how you know it worked: endpoints to probe, commands
  whose output proves health), **Teardown**. A runbook a reader cannot
  execute top-to-bottom is a bug.
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
