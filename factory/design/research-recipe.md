# Design Note: Research as a Recipe

**Status:** Phase 1 built: the Notes kind and `push-notes` (#1753); repo targets, `credentials: clone` and the `research` recipe.

A research conversation is a member talking to an agent about a repository: "how does the reconciler handle deletes?", "what changed in the last two weeks?". It runs in its own sandbox, and its notes can be saved to the member's fork on `research/notes`. Today it is built on a separate mechanism from recipes. This note moves it onto one: a built-in **`research` recipe** whose start clones the repo and asks the kickoff, whose conversation is the task session, and whose **Save notes** is a revise that produces a **Notes** task output with a **`push-notes`** action.

---

## Background & Problem Statement

Research came first (#1615–#1658), and built pieces that recipes later got in a general form:

| Research today | Recipes today |
|---|---|
| `factory research start`: sandbox `rsch-<slug>-<hash8>`, `ACPD_ENABLE=1`, clone, a `research-ready` receipt (#1654) | `factory recipe <name>`: sandbox, spool, `task.json`, daemon claims it |
| A kickoff annotation the controller sends with `CreateSession` + `Prompt` and deletes as the receipt; 1h TTL; the API refuses with 409 while it is owed | The start part's `ask` steps, run by the daemon; idempotent submit by `--run-name`, resume by the recorded run |
| acpd on :49984, reached by port-forward, the engine key in an `X-Engine-Api-Key` header | Daemon sessions on `/v1/sessions` (:49990), task tokens, held state, session/load (Continue session) |
| Capture: `POST /capture` stamps `research-capture` + `research-note` and prompts; the controller's `completeResearchSaves` runs `factory research save-notes` | Revises (`factory recipe revise`), task outputs, `factory apply` holding the token, Request verbs `revise` and `apply` |
| Prompts in `repo-agent/pkg/research` (onboard, activity, topic) | Prompts in the recipe |

So there are two ways to start an agent in a sandbox, two ways to send it a first turn, two session hosts, and two ways to turn a conversation into something published. Every fix lands twice or only in one (the kickoff race #1676 was research-only; resume by run name was recipe-only). Step 6 of the task rearchitecture (drop `ACPD_ENABLE` and :49984) can't happen while research uses them.

---

## Architecture

### The recipe

```yaml
name: research
task-type: research
credentials: clone          # see "Credentials" below
context: |
  You are helping a member of the project understand the repository
  checked out in the current directory. Answer from the code, and say
  plainly when you are guessing. …
inputs:
  topic: {description: What the member wants to understand, required: true}
start:
  steps:
    - uses: clone
    - uses: configure-engine
    - ask: |
        {{ .Inputs.topic }}

        For context: the repository {{ .Inputs.repo_owner }}/{{ .Inputs.repo_name }} … (topic.txt)
revise:
  - id: notes
    label: Save notes
    steps:
      - ask: |
          Write up what we found in this conversation as notes for someone
          reading them months from now. Respond with ONLY the notes as
          markdown: a title, a short summary, then the findings with file
          paths.
        capture: notes.md
outputs:
  - notes.md
task-output:
  kind: Notes
  from: notes.md
  actions:
    - {verb: edit, field: spec.markdown, format: markdown}
    - {verb: push-notes, label: Save to research/notes}
```

- **The kickoff prompt moves into the recipe.** Only topic's: the board sends kind topic and nothing else, so onboard and activity are not carried over. The board only sends inputs.
- **The conversation is the task session.** The start ends after the kickoff's reply; the session is closed and the member continues it, as Continue session does for plans (session/load). Nothing research-specific is needed to keep talking.
- **Start captures nothing.** A research task has no output until **Save notes**: a start that doesn't capture the task output's `from` gets no task output (and `recipe research` prints the revise to run instead). The validation rule "a recipe with revises asks at least once in `start`" holds; `outputs` is only what revises capture.
- **Save notes can be clicked again.** Each click is a revise, a new task with its own `notes.md` and task output, written from the whole conversation so far.

### A repo target

`runRecipe` needs an issue or PR URL today and fills `issue_*`. Research targets a repository:

```
factory recipe research --url <repo url> --topic … [--session ID] [--run-name NAME]
```

- **The URL may be a repo.** `githubItem` gains a repo-only form (`Number` 0) with the inputs `repo_owner`, `repo_name`, `url` and `repo_url`. Every ask is rendered with the inputs before a sandbox is made, so a recipe that reads `issue_*` fails then for a repo target.
- **One sandbox per conversation.** An issue recipe goes in the issue's sandbox (`fix-<repo>-N`). A repo recipe goes in the sandbox of its `--session`, named as a research session's is (`rsch-<slug>-<hash8>`, made by `EnsureResearchSandbox`), so the board passes the session id it has today and rows, rename and delete keep their keys. Without `--session` the run name is the session, else a new one. Once the task is handed over the sandbox gets the `research-ready` receipt, so it isn't taken for an interrupted launch's; both go in phase 3.
- **Not a side task.** `task-type: research` makes it the sandbox's main task, so `last-task-*` reports research.

### Credentials

Research holds a stricter rule than plan: **the GitHub token is never on the sandbox's disk.** The session may run with approvals off (`yolo`, `autoApprove`), so anything on the PVC can be read back by the agent. A plan sandbox doesn't hold this rule: `setup-git` writes gh `hosts.yml` and a global `url.insteadOf`.

A recipe declares which rule it holds:

- **`credentials: full`** (the default; plan, triage): as today.
- **`credentials: clone`**: the token reaches only the `clone` named step, for that one exec. `clone` is a new lib.sh function (`cloneRepo`), today's `researchCheckoutScript`: clone or fetch with `-c credential.helper='!gh auth git-credential'`, the token in the environment, and nothing written to disk. In such a recipe `setup-git`, `setup-repo` and `checkout-*` are refused at load time, and so is any recipe without exactly one `clone`, in start, before the first ask: no agent runs while the token is held.

**Not in the task's environment either.** A task's env becomes the runner's process environment, which the agent could read back from `/proc/<pid>/environ` for as long as the runner lives. So the CLI sends the token as the task's **secrets** (`PostRequest.Secrets`), not its env:

- The task server writes them to `secrets.json` (0600) in the task directory, never serves it, and doesn't export it to the process. A server that doesn't advertise `secrets` in `/v1/version`, and the envd spool, refuse a task with secrets: recreate the sandbox.
- The runner reads and deletes `secrets.json` first thing, refuses a `credentials: clone` recipe whose environment carries any GitHub token variable, gives the secrets to the `clone` step alone and forgets them once it has run.
- A revise sends no secrets: it has no `clone`.

The engine key stays in the environment: the agent holds it anyway. `configure-engine` writes `settings.json` only, no key.

### The Notes output and `push-notes`

```yaml
kind: Notes
spec:
  name: how-the-reconciler-deletes   # the file's name on the branch
  markdown: |
    # How the reconciler handles deletes
    …
source: {task: …, session: …}
actions: …
```

- **`push-notes` is an apply verb,** like `comment`: `factory apply --action push-notes` publishes. It writes `spec.markdown` to `docs-exploration/research/<name>.md` on `research/notes` in the member's fork, as one commit through GitHub's git data API, with the caller's token. It forks if the member has none, and starts the branch with no parent, as `save_notes.sh` does. Nothing runs in the sandbox, so the token never gets near it and a paused sandbox needn't wake. A branch that moved meanwhile (another conversation saved) is rebuilt on, a few times. Saving the same notes again changes nothing.
- **`name`** is the conversation's title (the board's rename or auto-title), which whoever keeps the draft sets in the document before applying it. Unset, it is the conversation's id (`source.session`, else `source.task`), as `NOTES_FILE` defaults to the session id today.
- **Saving needs two clicks:** Save notes writes the draft (a revise; never publishes), then Save to research/notes pushes it. This matches plan's Update plan → Post plan. A one-click mode (revise, then apply automatically) can come later if two clicks feel heavy.
- **Permissions:** `push-notes` writes to the member's own fork, so any member may apply it to their own conversation.

### repo-agent

- **Launch.** The Request verb `research` stays. The controller runs `factory recipe research --url <repo> --session <session> --run-name research/<session>/<ts> --topic …` instead of `factory research start` plus a kickoff. The kickoff annotation, its TTL, the 409, the `research-ready` receipt and `research-engine` all go. The recorded run says what ran, and the engine is the board's (claude still maps to gemini; see Known limits).
- **The rail.** `GET /api/research` lists research sandboxes as today. Live state (busy / waiting / held) comes from the daemon's sessions, as the task-session view's does. Pending rows still come from Requests.
- **Open.** A row opens the task-session view on its session. The research view's landing pane (box, overview, recent changes), terminal and mermaid rendering are kept and become how a research task session is shown.
- **Rename, delete, title.** Kept as they are: annotations on the sandbox, and deleting the sandbox.
- **Save notes.** The task-session view already shows the session's revise actions (#1751). For research that is **Save notes**. The resulting draft appears in the same view with **Save to research/notes**, applied through the Request verb `apply` (#1736). The revise Request and its controller code are keyed by issue number today; they gain a sandbox key for repo targets.
- **Removed:** `POST /capture`, `research-capture` / `research-note`, `completeResearchSaves`, the controller's kickoff path, the research `CreateSession` / `Prompt` client and its header.

---

## Decisions

- **maxActive and idle pause.** Research sandboxes count toward `maxActive` today, although comments say they don't, and they are never paused. As recipe tasks they are paused when idle like issue sandboxes, and woken when opened (session/load brings the conversation back). They still count toward `maxActive` while awake.
- **Mode.** `yolo` and auto-approve stay the research default, set on the session when it is created, as a recipe's are. `credentials: clone` is what makes that safe.
- **The warm pool is orthogonal.** A pooled sandbox needs a way to hand over a sandbox not named after the session (a `--sandbox` flag then); the recipe doesn't change.
- **No back-compat.** Existing research sandboxes are not migrated. They keep working until phase 3, then are deleted (with a yes). Saved notes on `research/notes` are untouched.

## Known limits

- **One engine per conversation.** As with revises, session/load works only on the engine the conversation started with.
- **Claude** still maps to gemini until acpd is engine-agnostic. This note doesn't change that.
- **Notes are written from the agent's view of the conversation,** like Update plan. A member wanting exact text edits the draft before pushing.

## Phases

1. **factory.** Repo targets in `factory recipe`; `credentials: clone` and the `clone` named step; the built-in `research` recipe with its kickoff templates; the Notes kind; the `push-notes` verb in `factory apply`.
2. **repo-agent.** Launch research through the recipe; rail and open on task sessions; Save notes as revise + apply; revises keyed by sandbox.
3. **Delete the old path.** Done: `factory research start` / `save-notes`, the kickoff and capture code in repo-agent, and the capture prompt in `repo-agent/pkg/research` are gone. A sandbox the old path made is still listed, marked legacy, so it can be deleted, but every conversation route answers 409. The kickoff templates stay, because the controller renders canned openings into the recipe's topic. Next is step 6 of the task rearchitecture: drop `ACPD_ENABLE` and the :49984 listener.
