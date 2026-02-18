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
| Area | Description | Key Directories |
|------|-------------|-----------------|
| **API Server** | Request handling, admission control, API discovery, OpenAPI | `staging/src/k8s.io/apiserver/`, `pkg/controlplane/` |
| **CRDs** | CRD lifecycle, validation, conversion webhooks, structural schemas | `staging/src/k8s.io/apiextensions-apiserver/` |
| **Server-Side Apply (SSA)** | Field management, merge strategies, field ownership | `staging/src/k8s.io/apimachinery/pkg/util/managedfields/` |
| **Admission Control** | VAP, MAP, webhooks | `plugin/pkg/admission/`, `staging/src/k8s.io/apiserver/pkg/admission/` |
| **CEL** | CEL in validation rules, admission policies, authorization | `staging/src/k8s.io/apiserver/pkg/cel/` |
| **API Machinery Libraries** | Types, serialization, conversion, defaulting, scheme | `staging/src/k8s.io/apimachinery/` |
| **client-go** | Informers, caches, reflectors, work queues, dynamic client | `staging/src/k8s.io/client-go/` |
| **Code Generators** | deepcopy-gen, conversion-gen, validation-gen, openapi-gen, etc. | `staging/src/k8s.io/code-generator/` |
| **API Aggregation** | Aggregated API servers, APIService resources | `staging/src/k8s.io/kube-aggregator/` |
| **Storage / Watch** | etcd backend, watch cache, WatchList, storage version migration | `staging/src/k8s.io/apiserver/pkg/storage/` |
| **APF** | Flow control, priority levels, flow schemas | `staging/src/k8s.io/apiserver/pkg/util/flowcontrol/` |
| **Feature Gates** | Feature gate framework, versioned feature gates, emulation versions | `staging/src/k8s.io/component-base/featuregate/` |
| **CLE** | LeaseCandidate, coordinated leader election | `staging/src/k8s.io/client-go/tools/leaderelection/` |
| **Declarative Validation** | validation-gen, +k8s:* tags, migration from handwritten validation | `staging/src/k8s.io/code-generator/cmd/validation-gen/` |
| **OpenAPI** | OpenAPI v2/v3 spec generation, publishing, aggregation | `staging/src/k8s.io/kube-openapi/` |

### Active Initiatives (2025-2026)
- **Declarative Validation (KEP-5073)**: Migrating ~15k lines of hand-written validation to IDL tags
- **WatchList / Streaming (KEP-3157)**: Streaming initial object sets via WATCH instead of LIST
- **Coordinated Leader Election**: Multi-candidate leader election with strategy-based selection
- **MutatingAdmissionPolicy**: CEL-based admission mutation (companion to VAP)
- **Compatibility Versions / Emulation Versioning**: Running newer binaries with older API behavior
- **Storage Version Migration**: Automated migration of stored resource versions

## Triage Decision Framework

**Before applying any labels, check the issue's existing labels.** Only add labels that are missing. Do not re-apply labels that are already present (e.g., if the issue already has `kind/bug` and `sig/api-machinery`, skip those commands in your triage comment).

### Step 1: Classify the Issue Type

| Kind | When to Use |
|------|-------------|
| `kind/bug` | Something is broken, not working as documented, incorrect results, panics, crashes. Most common. |
| `kind/feature` | New functionality, API changes, enhancements. Includes KEP proposals. |
| `kind/flake` | Test passes sometimes and fails other times. Should include CI links and triage board. |
| `kind/failing-test` | Test consistently failing (not intermittent). |
| `kind/cleanup` | Code hygiene, deprecated API removal, refactoring. No user-visible change. |
| `kind/regression` | Behavior that worked in a previous version but is now broken. |
| `kind/support` | User questions, confusion about expected behavior. |
| `kind/documentation` | Missing, incorrect, or unclear documentation. |
| `kind/api-change` | Proposed changes to the Kubernetes API surface. |

**Heuristics:**
- Title starts with "[Flaking Test]" → `kind/flake`
- Title starts with "[Failing test]" → `kind/failing-test`
- "What would you like to be added?" → `kind/feature`
- "How do I..." → `kind/support`
- Behavior differs from docs or API contracts → `kind/bug`
- Was passing before a specific PR/commit → `kind/regression`

### Step 2: Determine SIG Ownership

Every issue needs at least `/sig api-machinery`. Add co-owning SIGs when the issue spans multiple domains. Do NOT remove `sig/api-machinery` when adding other SIGs.

| Co-SIG | When to Add |
|--------|-------------|
| `sig/auth` | Admission policies, RBAC, authentication, TLS, EgressSelector |
| `sig/cli` | kubectl behavior related to API machinery (apply, diff, SSA) |
| `sig/instrumentation` | Metrics, tracing, logging from apiserver components |
| `sig/scalability` | Performance, memory, watch cache contention, APF tuning |
| `sig/network` | APIService connectivity, network-level API issues |
| `sig/testing` | Test infrastructure, integration test framework |
| `sig/apps` | Workload APIs when the issue is on the API machinery side |
| `sig/node` | kubelet API interactions, node-level field selectors |
| `sig/etcd` | etcd integration, storage backend, etcd version compatibility |
| `sig/scheduling` | Scheduler API interactions |
| `sig/storage` | StorageClass/PV APIs when on the API machinery side |

### Step 3: Evaluate for Acceptance

**Accept** (`/triage accepted`) when the issue has a clear bug with version info, a concrete technical proposal, real technical debt with defined scope, test flake/failure with CI links, or is filed by a known contributor with sufficient context.

**Request more info** when bug reports lack reproduction steps or version info, feature scope is unclear, or it's unclear which component is responsible.

**Close or redirect** when behavior is working-as-designed, issue is a duplicate, belongs to a different project, needs a KEP (redirect to kubernetes/enhancements), or is a support question for Slack/Stack Overflow.

### Step 4: Set Priority

Most issues rely on `triage/accepted` alone. Apply priority labels selectively:

| Priority | When to Use |
|----------|-------------|
| `priority/critical-urgent` | Release-blocking, data loss, security, CI-blocking failures |
| `priority/important-soon` | Regressions, bugs affecting production users |
| `priority/important-longterm` | Significant tech debt, multi-release initiatives |
| `priority/backlog` | Nice-to-have, minor cleanup |
| `priority/awaiting-more-evidence` | Feature requests without sufficient demand |

### Step 5: Assign Owners

| Domain Area | Key Maintainers |
|-------------|-----------------|
| CEL, Validation Rules, CRD Formats | @jpbetz, @cici37 |
| ValidatingAdmissionPolicy, MutatingAdmissionPolicy | @jpbetz, @cici37 |
| Server-Side Apply (SSA) | @jpbetz |
| Declarative Validation | @jpbetz, @thockin, @yongruilin |
| OpenAPI v2/v3 | @jpbetz, @Jefftree |
| Coordinated Leader Election | @Jefftree, @Henrywu573 |
| Compatibility Versions, Emulation | @Jefftree, @jpbetz |
| Code Generators | @Jefftree |
| API Aggregation | @Jefftree |
| Feature Gates, Versioned Feature Gates | @siyuanfoundation |
| Watch Cache, WatchList, Streaming | @serathius, @p0lyn0mial |
| Storage, etcd integration | @serathius |
| API Priority and Fairness (APF) | @MikeSpreworst |
| General API Server, Core Framework | @liggitt, @deads2k, @sttts |
| client-go, Informers | @deads2k |

Mark well-scoped issues with `/help wanted` or `/good-first-issue` for community contribution.

### Step 6: Manage Issue Lifecycle

- `/remove-lifecycle stale` — keep important issues from going stale
- `/lifecycle frozen` — protect umbrella issues and long-running discussions from auto-closure
- `/close` — close resolved or irrelevant issues with a brief explanation

## Slash Command Reference

```
/kind bug|feature|flake|failing-test|cleanup|regression|support|documentation
/sig api-machinery|auth|cli|instrumentation|...
/triage accepted|needs-information
/priority critical-urgent|important-soon|important-longterm|backlog|awaiting-more-evidence
/area apiserver|custom-resources|code-generation|client-libraries|admission-control|test
/assign [@username]
/cc @username
/help wanted
/good-first-issue
/lifecycle frozen|active
/remove-lifecycle stale|rotten
/remove-sig <sig-name>
/close
```

## Triage Comment Templates

**Accept:**
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

**Cross-SIG routing:**
```
This issue touches both API machinery and [other SIG].

/sig api-machinery
/sig <other-sig>
/cc @<other-sig-lead>
/triage accepted
```

**Close:**
```
This is working-as-designed because [explanation].

/close
```

**Remove sig/api-machinery:**
```
This issue is about [specific domain] rather than API server infrastructure. The [component] is owned by [other SIG].

/remove-sig api-machinery
/sig <correct-sig>
```

## When to Remove sig/api-machinery

> **SIG API Machinery owns API server infrastructure, conventions, serialization, and generic mechanisms — not every issue that involves a Kubernetes API resource or mentions the API server.**

The SIG that owns the **code path where the bug lives** is the correct SIG, not the SIG whose domain the symptom superficially resembles.

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
1. The buggy code lives outside api-machinery owned directories (`staging/src/k8s.io/{apiserver,apimachinery,client-go,apiextensions-apiserver,kube-aggregator}/`)
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
