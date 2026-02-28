---
name: apimachinery-issue-triage
description: >
  Use this skill when triaging issues in the kubernetes/kubernetes GitHub
  repository that belong to SIG API Machinery. This includes evaluating new
  issues, applying labels (kind, priority, area, sig), accepting or closing
  issues, assigning owners, routing cross-SIG issues, and managing issue
  lifecycle.
---

# SIG API Machinery Issue Triage

Triage Kubernetes issues for SIG API Machinery. Produce triage recommendations that match how experienced SIG API Machinery leads would handle each issue.

## SIG API Machinery Scope

### Core Domain Areas

API Server, CRDs, Server-Side Apply, Admission Control (VAP/MAP/webhooks), CEL, API Machinery Libraries (types/serialization/conversion), client-go, Code Generators, API Aggregation, Storage/Watch, APF, Feature Gates, Coordinated Leader Election, Declarative Validation, OpenAPI.

To determine which directories and OWNERS files are relevant for a given issue, check the OWNERS files in the kubernetes/kubernetes repo. The top-level SIG API Machinery OWNERS files are at:
- `staging/src/k8s.io/apiserver/OWNERS`
- `staging/src/k8s.io/apimachinery/OWNERS`
- `staging/src/k8s.io/client-go/OWNERS`
- `staging/src/k8s.io/apiextensions-apiserver/OWNERS`
- `staging/src/k8s.io/kube-aggregator/OWNERS`
- `staging/src/k8s.io/code-generator/OWNERS`

### Active Initiatives

For current active KEPs and initiatives, check the kubernetes/enhancements repo filtered by SIG API Machinery: https://github.com/kubernetes/enhancements/issues?q=is%3Aissue+is%3Aopen+label%3Asig%2Fapi-machinery

## Triage Decision Framework

**Before applying any labels, check the issue's existing labels.** Only add labels that are missing. Do not re-apply labels that are already present (e.g., if the issue already has `kind/bug` and `sig/api-machinery`, skip those commands in your triage comment).

### Step 1: Classify the Issue Type

Apply a `kind/*` label using `/kind <type>`. For the full list of available kinds, see https://prow.k8s.io/command-help#kind.

**Heuristics for common cases:**
- Title starts with "[Flaking Test]" → `kind/flake`
- Title starts with "[Failing test]" → `kind/failing-test`
- "What would you like to be added?" → `kind/feature`
- "How do I..." → `kind/support`
- Behavior differs from docs or API contracts → `kind/bug`
- Was passing before a specific PR/commit → `kind/regression`

### Step 2: Determine SIG Ownership

Every issue needs at least one SIG label. If the issue belongs to API Machinery, ensure `/sig api-machinery` is present. If the issue is mis-assigned and does not belong to API Machinery, remove it with `/remove-sig api-machinery` and assign the correct SIG (see "When to Remove sig/api-machinery" below). For cross-cutting issues, multiple SIGs can co-own.

**Commands:**
- `/sig api-machinery` — adds the `sig/api-machinery` label. Also clears `do-not-merge/needs-sig` if present.
- `/remove-sig api-machinery` — removes the `sig/api-machinery` label. Use when the issue does not belong to API Machinery.
- Multiple SIGs are fine: use `/sig api-machinery` + `/sig auth` in the same comment to co-own.

When the issue spans multiple domains, add co-owning SIGs. To determine the correct SIG, check the OWNERS files in the relevant code paths or refer to https://github.com/kubernetes/community/blob/master/sig-list.md.

### Step 3: Evaluate and Route

`/triage accepted` means "triage is done." Never mark an issue accepted without also routing it — otherwise it gets orphaned. Every accepted issue must have one of: (a) an assignee or `/cc` to a domain expert, (b) redirection to another SIG, or (c) a `/help wanted` label for community pickup.

**Accept and route** (`/triage accepted` + `/cc @<owner>`) when the issue has a clear bug with version info, a concrete technical proposal, real technical debt with defined scope, test flake/failure with CI links, or is filed by a known contributor with sufficient context.

**Request more info** when bug reports lack reproduction steps or version info, feature scope is unclear, or it's unclear which component is responsible.

**Close or redirect** when behavior is working-as-designed, issue is a duplicate, belongs to a different project, needs a KEP (redirect to kubernetes/enhancements), or is a support question for Slack/Stack Overflow.

**Routing:** To find the right reviewer, identify the relevant code path and check its OWNERS file in the kubernetes/kubernetes repo. Use `/cc @<reviewer>` to route.

Mark well-scoped issues with `/help wanted` or `/good-first-issue` for community contribution.

### Step 4: Set Priority

Most issues rely on `triage/accepted` alone. Apply priority labels selectively:

| Priority | When to Use |
|----------|-------------|
| `priority/critical-urgent` | Release-blocking, data loss, security, CI-blocking failures |
| `priority/important-soon` | Regressions, bugs affecting production users |
| `priority/important-longterm` | Significant tech debt, multi-release initiatives |
| `priority/backlog` | Nice-to-have, minor cleanup |
| `priority/awaiting-more-evidence` | Feature requests without sufficient demand |

### Step 5: Manage Issue Lifecycle

- `/remove-lifecycle stale` — keep important issues from going stale
- `/lifecycle frozen` — protect umbrella issues and long-running discussions from auto-closure
- `/close` — close resolved or irrelevant issues with a brief explanation

## Slash Command Reference

Full command documentation: https://prow.k8s.io/command-help

Commands most used during triage: `/kind`, `/sig`, `/remove-sig`, `/triage`, `/priority`, `/cc`, `/assign`, `/close`, `/lifecycle`, `/remove-lifecycle`, `/help wanted`, `/good-first-issue`.

## Triage Comment Templates

**Accept and route:**
```
/kind bug
/triage accepted
/cc @<domain-expert>
```

**Request info:**
```
Could you provide [specific missing details]?

/triage needs-information
```

**Reassign to another SIG:**
```
This issue is about [specific domain] rather than API server infrastructure. The [component] is owned by [other SIG].

/remove-sig api-machinery
/sig <correct-sig>
```

**Co-own with another SIG (rare):**
```
This issue touches both API machinery and [other SIG].

/sig <other-sig>
/cc @<other-sig-lead>
/triage accepted
```

**Close — working as designed:**
```
This is working-as-designed because [explanation].

/close
```

**Close — duplicate:**
```
Duplicate of #<number>.

/close
```

**Close — stale / not reproducible:**
```
This issue has not been reproducible on recent versions. Closing — please reopen if you can still reproduce on v1.XX.

/close
```

**Close — support question:**
```
This is a usage question rather than a bug or feature request. Please ask on the #sig-api-machinery Slack channel or Stack Overflow.

/close
```

**Close — needs KEP:**
```
This feature request needs a KEP. Please file a KEP in kubernetes/enhancements and link it here.

/close
```

## When to Remove sig/api-machinery

> **SIG API Machinery owns API server infrastructure, conventions, serialization, and generic mechanisms — not every issue that involves a Kubernetes API resource or mentions the API server.**

The SIG that owns the **code path where the bug lives** is the correct SIG, not the SIG whose domain the symptom superficially resembles.

### sig/api-machinery vs. API Review

- **sig/api-machinery** owns the infrastructure: the API server, request handling, admission, storage, serialization, code generators, client-go, CRD engine, and generic mechanisms that all APIs depend on.
- **API review** is a cross-cutting process. Any SIG proposing Kubernetes API changes (new fields, new resources, new versions) must go through API review, which is led by @thockin and @jpbetz. API review issues/PRs get the `api-review` label but do **not** necessarily belong to `sig/api-machinery` — the owning SIG is whichever SIG owns the API being changed (e.g., `sig/node` for Node API changes, `sig/apps` for Deployment API changes).
- Only add `sig/api-machinery` when the issue is about the API infrastructure itself, not simply because an API is involved.

### Removal Patterns

| Pattern | Signal | Correct SIG |
|---------|--------|-------------|
| **Kubelet/Node** | Unexpected value/format in a resource field, but the API type works correctly — the problem is in how kubelet populates the field | `sig/node` |
| **kubectl/CLI** | kubectl panic, crash, or behavioral problem; stack traces contain `k8s.io/apimachinery/` but bug is in kubectl code | `sig/cli` |
| **Test ownership** | Test name contains "sig-api-machinery" or test is in `test/e2e/apimachinery/`, but the failure is in code owned by another SIG. Test cases belong to the SIG whose **code is under test**, not the test suite name. | Varies |
| **Scheduling** | Pod scheduling behavior, `schedulerName` handling, scheduler observability — even if an admission-based solution is discussed | `sig/scheduling` |
| **Storage/PV** | PersistentVolumes, StorageClasses, CSI drivers — even if the test path contains "apimachinery" | `sig/storage` |
| **Cloud provider** | `cloud-controller-manager` images, cloud-specific infrastructure | `sig/cloud-provider` |
| **etcd** | etcd-specific behavior (compaction, defrag, version compatibility). If it's about the apiserver's storage abstraction layer, it may still be api-machinery. | `sig/etcd` |
| **Network/Service** | Service environment variables, DNS, network behavior — even if it involves API defaulting or admission for network fields | `sig/network` |
| **Instrumentation** | Workqueue metrics, logging patterns, observability — metric stability graduation belongs to sig/instrumentation regardless of which package the metrics live in | `sig/instrumentation` |
| **Infrastructure** | TCP resets, TLS handshake failures, or other transport-layer issues that manifest during API calls | `sig/scalability` or `sig/network` |

### Decision Checklist

Remove `sig/api-machinery` if ALL of these are true:
1. The relevant code lives outside api-machinery owned directories (`staging/src/k8s.io/{apiserver,apimachinery,client-go,apiextensions-apiserver,kube-aggregator}/`)
2. The fix would be made in code owned by another SIG
3. The issue is about a specific resource's behavior, not generic API mechanisms
4. The API server is just the surface where the symptom appears, not the root cause

### Common False Signals

Do NOT assign `sig/api-machinery` based on:
- Stack trace contains `k8s.io/apimachinery/` (shared library used by all components)
- Test is in `test/e2e/apimachinery/` (test location != code ownership)
- Issue mentions "API server" (the API server is the interface for everything)
- Issue involves an API object like Pod or Service (every k8s feature uses API objects)
- `kubectl` command produced an error (`sig/cli`)
- `cloud-controller-manager` is mentioned (`sig/cloud-provider`)
- Error occurred during etcd operations (`sig/etcd`)
