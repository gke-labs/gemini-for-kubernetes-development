# repo-agent — Demo Script & Storyboard

A short screen-recorded video (~3 minutes), text overlays, no voice,
tell → show → tell per scene. The slides mirror the scenes 1:1 and
double as the storyboard.

**The spine (every overlay serves this one line):**

> From stranger to contributor to operator on any repo —
> agents do the legwork, git keeps the receipts.

Two chapters, two personas: **arriving** at an unfamiliar repo, and
**maintaining** the ones you own.

---

## Script

### Scene 0 — Cold open (6s, text on black)

> `New to a repo — where do you start?`
> *(beat)*
> `Maintaining one — drowning in the queue?`
> *(beat)*
> `repo-agent.`

---

### Chapter 1 — Arriving

#### Scene 1 — One board (12s)

- **Tell:** `Point it at any repo.`
- **Show:** Paste a repo URL → the board appears → Up Next: issues,
  review requests, PRs — each row with a single button.
- **Tell:** `One board. One action per row.`

#### Scene 2 — Understand it (18s)

- **Tell:** `First: understand it. One click.`
- **Show:** Explore tab → **Generate Overview** → cut to the docs list
  → open `architecture.md` inline → scroll a rendered mermaid diagram
  → flash **What happened ▾ → last 2 weeks** and highlight the
  "maintainer asks" section of the digest.
- **Tell:** `Architecture, code map, recent activity — written to YOUR
  fork, in git. Not a chat window.`

#### Scene 3 — Make it runnable (18s)

- **Tell:** `Now make it runnable.`
- **Show:** **Draft Runbooks** → `deploy-gcp.md` in the inline viewer,
  scroll **What this needs** ("can this run in the pod, or does it
  need real infrastructure?") → Runs tab → composer → **▶ Run** →
  queued chip → row flips to ⚙ running → time-cut → **✅ verified**.
- **Tell:** `Runbooks derived from the repo's own tooling — never
  invented. Then actually run: scripts pushed to git BEFORE they
  execute.`

#### Scene 4 — Receipts & teardown (12s)

- **Tell:** `Don't trust it. Verify it.`
- **Show:** **receipt ↗** → `VERIFIED` on the first line → scroll to
  "what is left RUNNING" → back to the table → **Tear down** →
  🔻 torn down badge.
- **Tell:** `Verdict. Evidence. Teardown checklist. Nothing left
  behind.`

---

### Chapter 2 — Maintaining

#### Scene 5 — Your inbox, not your backlog (12s)

- **Tell:** `And the repos you already own?`
- **Show:** The **All** board — needs-you badges on the board tabs →
  the aggregated Up Next, rows tagged by board.
- **Tell:** `Every repo you maintain. Only what needs YOU.`

#### Scene 6 — Triage & plan (18s)

- **Tell:** `New issue? The agent goes first.`
- **Show:** Issue row → **Triage ready** → open the verdict panel →
  **Plan** → time-cut → **Plan ready** → open the plan → **Approve**.
- **Tell:** `It triages and plans. You read and approve. Then it
  codes.`

#### Scene 7 — Fix → PR (15s)

- **Tell:** `Approved fixes become pull requests.`
- **Show:** Issue with the **Fix ✓↗** receipt chip → the draft PR it
  created → the own-draft row in Up Next → **Promote PR**.
- **Tell:** `Fixes arrive as draft PRs. Promote when they're ready.`

#### Scene 8 — Review (15s)

- **Tell:** `Review requested?`
- **Show:** Red-tinted **Review** button on a requested-review row →
  the agent's review in the verdict panel → the pending review parked
  on GitHub, awaiting submission.
- **Tell:** `Reviews drafted and parked as pending on GitHub. YOU
  click submit. Always.`

#### Scene 9 — Your PRs work themselves (18s)

- **Tell:** `And your own PRs?`
- **Show:** My PRs row → **Agent ▾** drawer: *Address review comments /
  Fix failing CI / Iterate* with a typed instruction → the per-PR
  auto ⏻ toggle → the Running chip ("addressing…").
- **Tell:** `Comments addressed. CI fixed. Iterations on request — or
  on auto, per PR. Watched until merge.`

#### Scene 10 — Guardrails & engines (12s)

- **Tell:** `Autonomy, on your terms.`
- **Show:** Board settings: auto tiers (assigned / labeled), the
  maxActive limit → two boards side by side with gemini and claude
  engine icons.
- **Tell:** `Auto only where you allow it. Capped. Pick your engine
  per board.`

---

### Scene 11 — Trust (10s)

- **Tell:** `And your cloud?`
- **Show:** Settings → GCP section: the Workload Identity principal
  and the copy-paste grant command → zoom the line "No credentials are
  stored."
- **Tell:** `No keys. Anywhere. You grant access in YOUR project —
  and revoke it with one command.`

### Scene 12 — Close (8s, text on black)

> `Arrive. Understand. Run. Maintain.`
> `Agents do the legwork. Git keeps the receipts.`
> **`repo-agent`**

---

## Storyboard (slides mirror the scenes 1:1)

| #  | Chapter  | Slide          | Visual                                  | Key line                            |
|----|----------|----------------|-----------------------------------------|-------------------------------------|
| 0  | —        | The problem    | Black, two questions                    | "New to a repo? Drowning in one?"   |
| 1  | Arrive   | One board      | Up Next screenshot                      | "One action per row"                |
| 2  | Arrive   | Understand     | architecture.md + mermaid inline        | "Docs in YOUR fork"                 |
| 3  | Arrive   | Run it         | Runs table flipping to ✅ verified       | "Scripts pushed before they execute"|
| 4  | Arrive   | Receipts       | VERIFIED receipt + 🔻 torn down          | "Nothing left behind"               |
| 5  | Maintain | The inbox      | All board, needs-you badges             | "Only what needs YOU"               |
| 6  | Maintain | Triage & plan  | Verdict panel + plan approval           | "It plans. You approve. It codes."  |
| 7  | Maintain | Fix → PR       | Draft PR + Promote                      | "Fixes arrive as PRs"               |
| 8  | Maintain | Review         | Red Review button + pending on GitHub   | "YOU click submit. Always."         |
| 9  | Maintain | Your PRs       | Agent ▾ drawer + per-PR auto ⏻          | "Watched until merge"               |
| 10 | Maintain | Guardrails     | Auto tiers, maxActive, engine icons     | "Auto where you allow it"           |
| 11 | —        | Trust          | Workload Identity principal in Settings | "No keys, anywhere"                 |
| 12 | —        | Close          | Black, value prop                       | The spine, two lines                |

---

## Recording notes

- **Pre-stage everything.** Long-running work is cut, not waited for:
  have the explore sandbox parked with docs already on the branch
  (Scene 2 records the click, cuts, shows the ready state), and record
  Scene 3's run as click + queued chip live, splicing the ✅ state in —
  the receipt timestamp keeps it honest.
- **Chapter 2 checklist** (park these on a board before recording): a
  fresh triage-able issue, a plan awaiting approval, a draft PR born
  from a fix, an incoming review request, and an own PR with
  unaddressed comments.
- **Dark mode** records better with the chip palette.
- **Tear down last** (Scene 4's teardown can be recorded at the end of
  the session): the demo finishes by cleaning up its own cloud
  resources.
