---
name: apimachinery-pr-triage
description: >
  Use this skill when triaging pull requests in the kubernetes/kubernetes GitHub
  repository that belong to SIG API Machinery. This includes evaluating new PRs,
  applying labels, routing to reviewers, managing the review lifecycle, handling
  API review requirements, and ensuring merge readiness.
---

# SIG API Machinery PR Triage

Triage Kubernetes pull requests for SIG API Machinery. Produce triage recommendations that match how experienced SIG API Machinery leads would handle each PR.

## PR Lifecycle

### Merge Requirements

A PR needs all of these to merge via Tide:

| Label | How Applied |
|-------|-------------|
| `lgtm` | Reviewer posts `/lgtm`. Auto-removed on new commits (tied to git tree hash). |
| `approved` | `k8s-ci-robot` applies when all required OWNERS files have an approver. |
| `cncf-cla: yes` | Automatic CLA check. |
| `release-note` or `release-note-none` | From the `release-note` block in the PR body. |
| `kind/*` | At least one kind label. |
| `sig/*` | At least one SIG label. |

### Blocking Labels

| Label | Applied By | Cleared By |
|-------|-----------|------------|
| `do-not-merge/hold` | `/hold` | `/hold cancel` |
| `do-not-merge/work-in-progress` | PR title starts with `[WIP]` | Remove `[WIP]` from title |
| `do-not-merge/needs-sig` | Missing SIG label | `/sig <name>` |
| `do-not-merge/release-note-label-needed` | Missing release-note block | Add `release-note` block to PR body |
| `needs-rebase` | Merge conflicts detected | Rebase the PR |

### API Review

PRs with `kind/api-change` trigger `k8s-triage-robot` to post an API review reminder. Route to formal API review with `/label api-review` after the change is reviewed for completeness.

## PR Triage Decision Framework

**Before applying any labels, check the PR's existing labels.** Only add labels that are missing. Do not re-apply labels that are already present (e.g., if the PR already has `kind/bug` and `sig/api-machinery`, skip those commands in your triage comment).

### Step 1: Classify the PR Type

Apply a `kind/*` label using `/kind <type>`. For the full list of available kinds, see https://prow.k8s.io/command-help#kind.

### Step 2: Verify SIG Labels

Every PR needs at least one SIG label. If the PR belongs to API Machinery, ensure `/sig api-machinery` is present. If the PR is mis-assigned, remove with `/remove-sig api-machinery` and assign the correct SIG.

**Commands:**
- `/sig api-machinery` — adds the label. Also clears `do-not-merge/needs-sig`.
- `/remove-sig api-machinery` — removes the label. Use when the PR does not belong to API Machinery.

When the PR spans multiple domains, add co-owning SIGs. To determine the correct SIG, check the OWNERS files in the relevant code paths or refer to https://github.com/kubernetes/community/blob/master/sig-list.md.

Area labels are typically applied by the PR author or auto-detected — do not add them during triage.

### Step 3: Evaluate and Route

`/triage accepted` means "triage is done." Never mark a PR accepted without also routing it — otherwise it gets orphaned. Every accepted PR must have a reviewer assigned via `/cc @<reviewer>` or `/assign @<reviewer>`.

**Accept and route** (`/triage accepted` + `/cc @<reviewer>`) when the PR has a clear description, linked issue/KEP, tests, and is from a contributor with context. Triage and route in the same comment — don't wait.

**Request more info** when the PR lacks description, test coverage, or linked issue/KEP for feature work.

**Hold** (`/hold`) when the PR depends on another PR, needs rebase, or needs KEP approval first. Use `/hold` proactively to prevent wasted CI cycles on PRs that clearly need a rebase or rework.

**Fix incorrect labels** with `/remove-kind <wrong>` and `/kind <correct>`, or `/remove-sig <wrong>` and `/sig <correct>`.

**Routing:** To find the right reviewer, identify the relevant code path and check its OWNERS file in the kubernetes/kubernetes repo. Use `/cc @<reviewer>` to route.

### Step 4: Handle First-Time Contributors

For PRs from `FIRST_TIME_CONTRIBUTOR` or non-org-members:
1. Verify the PR is safe to test (no malicious code in test scripts, Makefiles, or generated code)
2. Use `/ok-to-test` to allow CI to run
3. Route to the domain expert with `/cc @<reviewer>`
4. Accept with `/triage accepted`
5. If the PR is trivial but useful, suggest the contributor batch similar fixes into one PR


## Slash Command Reference

Full command documentation: https://prow.k8s.io/command-help

Commands most used during PR triage: `/kind`, `/sig`, `/remove-sig`, `/triage`, `/cc`, `/assign`, `/hold`, `/hold cancel`, `/ok-to-test`, `/label api-review`, `/remove-kind`, `/close`.

## Triage Comment Templates

**Accept and route to reviewer:**
```
/kind <type>
/triage accepted
/cc @<domain-expert>
```

**First-time contributor:**
```
/ok-to-test
/kind <type>
/triage accepted
/cc @<domain-expert>
```

**Delegate review to specific person:**
```
@<reviewer> Would you review this?

/triage accepted
```

**Reassign to another SIG:**
```
This PR is about [specific domain] rather than API server infrastructure.

/remove-sig api-machinery
/sig <correct-sig>
```

**Hold for dependency:**
```
This PR depends on #<number>. Holding until that merges.

/hold
```

**Hold for rebase:**
```
This needs to be rebased to latest main.

/hold
```

**Route to API review:**
```
The API changes look complete. Routing to formal API review.

/label api-review
```
**Cross-SIG routing (rare):**
```
This PR touches both API machinery and [other SIG].

/sig <other-sig>
/cc @<other-sig-reviewer>
```

**Fix incorrect labels:**
```
/remove-kind <wrong>
/kind <correct>
```

**Close:**
```
Closing because [reason].

/close
```
