# One session view for every recipe

**Status:** Proposed. Step 1 (factory records the recipe's revises) is in this change.

Research has been a recipe since #1755, and a research conversation is a daemon task session, like a plan's or a triage's after *Continue session*. The board still shows the two differently. The browser uses one component for both, but it runs two mechanisms side by side:

| | Plan session | Research session |
|---|---|---|
| API | `/api/task-sessions/:sandbox/:task/*` | `/api/research/:session/*`, an event stream of its own |
| Revise button | built from the output's `revise` actions (*Update plan*) | a hard-wired 💾 that files the `notes` revise |
| Filed on | the issue's row (`/api/board/…/issues/N/actions/revise`) | the sandbox (`…/capture`) |
| Draft | on the board row only | a notes panel of its own, with `/notes` routes |

The special case exists because revise buttons come from the task output (`Recipe.OutputDecl` adds one action per revise). Research writes no output until its first Save notes, so there was nothing to build the button from.

This note makes the session view generic: what it shows comes from the recipe and the session's task output, never from which recipe it is.

## The design

### factory: the recorded run carries the recipe's revises (step 1)

`factory recipe` records the run on the sandbox (`<task-type>-run`). The record gains the recipe's revises:

```json
{"name": "…", "task": "…", "startedAt": "…",
 "revises": [{"id": "notes", "label": "Save notes"}]}
```

A session knows its buttons from the moment it starts, whether or not an output exists yet. The output's `revise` actions stay as they are; they are what `factory apply --action revise:<id>` reads.

### repo-agent: one task-session API

- **Research rows are task sessions.** A research row names its sandbox and task, and opening it opens `/api/task-sessions/:sandbox/:task`. The `/api/research/:session/{prompt,permission,cancel,mode}` routes and the research event stream are deleted. `/api/research` stays as the list, the launcher and delete.
- **`GET` returns `revises` and `draft` for any recipe.**
  - `revises` come from the recorded run of the session, each marked with what its newest revise Request says (revising, or why it failed), as today's plan revises are.
  - `draft` is the session's latest task output, whatever its kind: `kind`, `markdown`, the `actions` it offers, and the state of the clicks on it.
  - A plan's draft is the issue row's. A research draft is the sandbox's.
- **One way to act.** `POST …/revise {revise}` and `POST …/draft/:verb` file the Request. The server chooses the target, so the browser never does: the issue row when the sandbox has an issue, otherwise the sandbox (`spec.sandbox`, as Save notes does today). Permissions are the action's, as on the row.
- **Deleted:** `/research/:session/capture` and `/research/:session/notes{,/save}`. Draft editing and discarding become the draft's `edit` and `reject` actions.

### UI: one session view

- The buttons are `revises`: *Save notes* and *Update plan* come from the same code.
- The draft panel is the output's actions: *Save to research/notes*, *Post plan*, *Fix with this plan*, edit, reject. A plan session gains the draft panel, and the board row keeps its controls.
- Research-only, because they are about starting and finding conversations, not having one: the rail, the landing pane with the canned topics, and the rename.

## Steps

1. **factory (this change).** `revises` in the recorded run.
2. **repo-agent API.** `revises` and `draft` in the task-session `GET`, the generic `revise` and `draft/:verb` routes, research rows addressed by sandbox and task.
3. **UI.** One view from `revises` and `draft`. The 💾, the notes panel and the `/research/:session/*` session routes are deleted.

Sandboxes started before step 1 have no `revises` on their run, so they show no buttons. They are recreated rather than handled (no compatibility shims for task sessions).
