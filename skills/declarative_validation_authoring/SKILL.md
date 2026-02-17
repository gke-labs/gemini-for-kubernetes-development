---
name: declarative-validation-authoring
description: >
  Use this skill when authoring, migrating, enabling, or working on code changes
  to add declarative validation to a Kubernetes API. This includes modifying
  types.go files with validation tags, updating strategy.go files, marking
  handwritten validation for migration, writing declarative validation tests,
  and running code generation.
---

# Declarative Validation Authoring Expert

You are an expert in authoring Kubernetes Declarative Validation (DV) for APIs — both migrating existing handwritten validation and adding DV to new APIs. You possess deep knowledge of the validation-gen code generator, the migration process, the validation lifecycle, and the code patterns required for correct implementation.

## KEP-5073 Background

Declarative Validation (DV) replaces the ~15k lines of hand-written validation in the Kubernetes codebase with IDL tags (`+k8s:*`) placed directly on API type definitions in `types.go` files. A code generator called `validation-gen` reads these tags and produces validation code that is functionally equivalent to the hand-written validation.

**Key architecture:**
- Tags are placed on **versioned types** in `staging/src/k8s.io/api/<group>/<version>/types.go`
- Tags must **NOT** be placed on internal types in `pkg/apis/<group>/types.go`
- Generated code lives alongside the types
- During migration, both hand-written validation (HV) and declarative validation (DV) run in parallel
- Mismatch detection tracks differences via `declarative_validation_mismatch_total` metric

**Feature gates:**
- `DeclarativeValidation`: Enables the DV code path (runs DV alongside HV)
- `DeclarativeValidationBeta` (default: **on**): Controls enforcement of Beta-stage DV rules. When enabled, Beta DV rules are authoritative and corresponding HV errors are filtered. Replaces the deprecated `DeclarativeValidationTakeover`.

## Validation Lifecycle

DV uses a stability-based lifecycle for validation rules, controlled by tag prefixes:

### Alpha (`+k8s:alpha`)
```go
// +k8s:alpha(since:v1.36)=+k8s:minimum=0
```
- **Shadow mode only**: DV runs alongside HV but DV errors are never returned to clients
- Mismatches are recorded via metrics only
- HV remains fully authoritative
- Alpha validation errors **cannot** be tested via `strategy.go` (they're never in the output); rely on mismatch metrics for correctness verification
- Used when first migrating an existing validation to DV

### Beta (`+k8s:beta`)
```go
// +k8s:beta(since:v1.37)=+k8s:minimum=0
```
- **Enforced by default** when `DeclarativeValidationBeta` gate is enabled
- Corresponding HV errors are filtered out (DV is authoritative)
- Users can disable by turning off the `DeclarativeValidationBeta` gate
- When promoting from Alpha to Beta, test cases need updating

### Stable (no prefix)
```go
// +k8s:minimum=0
```
- **Permanently enforced** - no gate can disable it
- The corresponding HV code should be **removed** (not just marked)
- If HV is not removed, mismatch detection will catch duplicate errors

### New APIs (no handwritten fallback)
For brand-new APIs that never had hand-written validation:
- Use tags **without** any lifecycle prefix (they are stable from the start)
- No need for the `rest.WithDeclarativeEnforcement()` option since there's no HV to coordinate with

### Strategy File Plumbing
- For migration: use `rest.ValidateDeclarativelyWithMigrationChecks`
- For lifecycle-aware resources: pass `rest.WithDeclarativeEnforcement()` as a variadic option to `rest.ValidateDeclarativelyWithMigrationChecks`
- Do **not** use the deprecated `rest.ValidateDeclarativelyWithRecovery`

## Complete Validation Tag Reference

| Tag | Description | Scope | Supported Go Types | Example |
| :--- | :--- | :--- | :--- | :--- |
| `+k8s:customUnique` | Disables generated uniqueness validation (must be used with `+k8s:listType`). | Field, Type | `[]any`, `*[]any` | `// +k8s:listType=map`<br>`// +k8s:customUnique` |
| `+k8s:eachKey` | Applies validation to every key in a map. | Field, Type | `map[K]V`, `*map[K]V` | `// +k8s:eachKey=+k8s:maxLength=32` |
| `+k8s:eachVal` | Applies validation to every value in a list or map. | Field, Type | `[]T`, `*[]T`, `map[K]V` | `// +k8s:eachVal=+k8s:minimum=1` |
| `+k8s:enum` | Marks a string type as an enumeration. | Type | `string`, `*string` | `// +k8s:enum` |
| `+k8s:enumExclude` | Excludes a constant from the enum values. | Const | const of enum type | `// +k8s:enumExclude` |
| `+k8s:forbidden` | Indicates that a field must NOT be specified. | Field | Any Go type | `// +k8s:forbidden` |
| `+k8s:format` | Validates string conforms to a specific format. | Field, Type, ListVal, MapKey | `string`, `*string` | `// +k8s:format=k8s-uuid` |
| `+k8s:ifDisabled` | Applies validation only if feature is disabled. | Field, Type | Any Go type | `// +k8s:ifDisabled(MyGate)=+k8s:required` |
| `+k8s:ifEnabled` | Applies validation only if feature is enabled. | Field, Type | Any Go type | `// +k8s:ifEnabled(MyGate)=+k8s:required` |
| `+k8s:immutable` | The field cannot be changed after creation. | Field, Type | Any Go type | `// +k8s:immutable` |
| `+k8s:item` | Validates a specific item in a `listType=map` list. | Field, ListVal | `[]struct{...}` | `// +k8s:item(type="A")=+k8s:zeroOrOneOfMember` |
| `+k8s:listMapKey` | Field name(s) to use as key for `listType=map`. | Field, Type | `[]struct{...}` | `// +k8s:listMapKey=name` |
| `+k8s:listType` | Defines list behavior for SSA and validation. | Field, Type | `[]any`, `*[]any` | `// +k8s:listType=map` |
| `+k8s:maxItems` | Limits the maximum number of items in a list. | Field, Type | `[]any`, `*[]any` | `// +k8s:maxItems=1000` |
| `+k8s:maxLength` | Specifies the maximum length for a string. | Field, Type, MapKey | `string`, `*string` | `// +k8s:maxLength=253` |
| `+k8s:maxProperties` | Limits the maximum number of keys in a map. | Field, Type | `map[K]V` | `// +k8s:maxProperties=16` |
| `+k8s:minimum` | Specifies the minimum allowed value for an integer. | Field, Type | `int`, `int32`, etc. | `// +k8s:minimum=0` |
| `+k8s:neq` | Verifies value is not equal to specific value. | Field, Type | `string`, `int`, `bool` | `// +k8s:neq="Forbidden"` |
| `+k8s:opaqueType` | Ignores validation defined on the referenced type. | Field | Any Go type | `// +k8s:opaqueType` |
| `+k8s:optional` | Indicates that a field is optional. | Field | Any Go type | `// +k8s:optional` |
| `+k8s:required` | Indicates that a field must be specified. | Field | Any Go type | `// +k8s:required` |
| `+k8s:subfield` | Targets a subfield of a struct for validation. | Type, Field | Any Go type | `// +k8s:subfield(name="foo")=+k8s:optional` |
| `+k8s:unionDiscriminator` | Field determining active union member. | Field | Any | `// +k8s:unionDiscriminator` |
| `+k8s:unionMember` | Marks a field as a member of a union. | Field | Any | `// +k8s:unionMember` |
| `+k8s:unique` | Enforces uniqueness of items in a list. | Field, Type | `[]any`, `*[]any` | `// +k8s:unique=set` |
| `+k8s:update` | Constrains how a field can be updated. | Field | Any | `// +k8s:update=NoModify` |
| `+k8s:validateError` | Custom error message. | Field, Type | Any | `// +k8s:validateError="msg"` |
| `+k8s:validateFalse` | Field must be false. | Field, Type | `bool` | `// +k8s:validateFalse` |
| `+k8s:validateTrue` | Field must be true. | Field, Type | `bool` | `// +k8s:validateTrue` |
| `+k8s:zeroOrOneOfMember` | Exclusive choice (at most one set). | Field | Any | `// +k8s:zeroOrOneOfMember` |

**Supported `+k8s:format` values:** `k8s-ip`, `k8s-uuid`, `k8s-label-key`, `k8s-label-value`, `k8s-short-name`, `k8s-long-name`, `k8s-path-segment-name`, `k8s-resource-fully-qualified-name`, `k8s-resource-pool-name`, `k8s-extended-resource-name`

## Strategy Code Patterns

### Standard migration pattern
```go
// In Validate(ctx, obj)
allErrs := corevalidation.ValidateReplicationController(controller, opts)
return rest.ValidateDeclarativelyWithMigrationChecks(ctx, legacyscheme.Scheme, controller, nil, allErrs, operation.Create)

// In ValidateUpdate(ctx, obj, old)
errs := corevalidation.ValidateReplicationControllerUpdate(newRc, oldRc, opts)
return rest.ValidateDeclarativelyWithMigrationChecks(ctx, legacyscheme.Scheme, newRc, oldRc, errs, operation.Update)
```

### Lifecycle-aware pattern (Alpha/Beta/GA)
```go
// In Validate(ctx, obj)
allErrs := validation.ValidateWorkload(workload)
return rest.ValidateDeclarativelyWithMigrationChecks(ctx, legacyscheme.Scheme, obj, nil, allErrs, operation.Create, rest.WithDeclarativeEnforcement())

// In ValidateUpdate(ctx, obj, old)
allErrs := validation.ValidateWorkloadUpdate(newWorkload, oldWorkload)
return rest.ValidateDeclarativelyWithMigrationChecks(ctx, legacyscheme.Scheme, obj, old, allErrs, operation.Update, rest.WithDeclarativeEnforcement())
```

**Key imports:** `k8s.io/apimachinery/pkg/api/operation`, `k8s.io/apiserver/pkg/registry/rest`

**Important:**
- Do NOT call both imperative `Validate` and `ValidateUpdate` redundantly in the `ValidateUpdate` path — the DV framework handles create vs update internally.
- Remove unused imports for `k8s.io/apiserver/pkg/util/feature` and `k8s.io/kubernetes/pkg/features` if no longer needed.

## Test Template

DV tests go in `declarative_validation_test.go` alongside the `strategy.go` file. Use this structure:

```go
package <package_name>

import (
	"testing"
	"k8s.io/apimachinery/pkg/util/validation/field"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	apitesting "k8s.io/kubernetes/pkg/api/testing"
	api "<path_to_internal_api_package>"
)

// Order versions semantically: v1alpha1, v1beta1, v1
var apiVersions = []string{"<version1>", "<version2>"}

func TestDeclarativeValidate(t *testing.T) {
	for _, apiVersion := range apiVersions {
		t.Run(apiVersion, func(t *testing.T) {
			testDeclarativeValidate(t, apiVersion)
		})
	}
}

func testDeclarativeValidate(t *testing.T, apiVersion string) {
	ctx := genericapirequest.WithRequestInfo(genericapirequest.NewDefaultContext(), &genericapirequest.RequestInfo{
		APIGroup:   "<group>",
		APIVersion: apiVersion,
		Resource:   "<resource_plural>",
	})

	// Unified map for both valid and invalid cases.
	// Cases with nil expectedErrs are success cases.
	testCases := map[string]struct {
		input        api.<Kind>
		expectedErrs field.ErrorList
	}{
		"valid": {
			input: mkValid<Kind>(),
		},
		"zero <field>": {
			input: mkValid<Kind>(func(obj *api.<Kind>) { obj.Spec.<Field> = 0 }),
		},
		"negative <field>": {
			input: mkValid<Kind>(func(obj *api.<Kind>) { obj.Spec.<Field> = -1 }),
			expectedErrs: field.ErrorList{
				field.Invalid(field.NewPath("spec.<field>"), nil, "").WithOrigin("minimum"),
			},
		},
	}
	for k, tc := range testCases {
		t.Run(k, func(t *testing.T) {
			apitesting.VerifyValidationEquivalence(t, ctx, &tc.input, Strategy.Validate, tc.expectedErrs)
		})
	}
}

func TestDeclarativeValidateUpdate(t *testing.T) {
	for _, apiVersion := range apiVersions {
		t.Run(apiVersion, func(t *testing.T) {
			testDeclarativeValidateUpdate(t, apiVersion)
		})
	}
}

func testDeclarativeValidateUpdate(t *testing.T, apiVersion string) {
	ctx := genericapirequest.WithRequestInfo(genericapirequest.NewDefaultContext(), &genericapirequest.RequestInfo{
		APIGroup:   "<group>",
		APIVersion: apiVersion,
		Resource:   "<resource_plural>",
	})
	testCases := map[string]struct {
		update       api.<Kind>
		old          api.<Kind>
		expectedErrs field.ErrorList
	}{
		"valid": {
			update: mkValid<Kind>(),
			old:    mkValid<Kind>(),
		},
	}
	for k, tc := range testCases {
		t.Run(k, func(t *testing.T) {
			// Set ResourceVersion in the loop, not in mk helpers
			tc.old.ResourceVersion = "1"
			tc.update.ResourceVersion = "2"
			apitesting.VerifyUpdateValidationEquivalence(t, ctx, &tc.update, &tc.old, Strategy.ValidateUpdate, tc.expectedErrs)
		})
	}
}

// mkValid<Kind> creates a valid object with optional tweakers.
func mkValid<Kind>(tweakers ...func(*api.<Kind>)) api.<Kind> {
	obj := api.<Kind>{
		// ... minimal valid object ...
	}
	for _, tweak := range tweakers {
		tweak(&obj)
	}
	return obj
}
```

## Common Authoring Pitfalls

### Tag Placement
1. Tags go on **versioned types** in `staging/src/k8s.io/api/<group>/<version>/types.go` ONLY. Do NOT add tags to internal types in `pkg/apis/<group>/types.go`.
2. `+k8s:required` must be paired with `+required`. `+k8s:optional` must be paired with `+optional`. Convention: legacy tag first (`+required` then `+k8s:required`).
3. `+k8s:format` does NOT guarantee length enforcement. If HV checks length, you still need `+k8s:maxLength` alongside the format tag.
4. When migrating list fields, consider adding `+k8s:listType=atomic` alongside other validations.
5. If an entire spec is immutable, apply `+k8s:immutable` on the spec field itself rather than individual subfields.

### Handwritten Validation Marking
1. Never delete HV files. Mark covered errors with `.MarkCoveredByDeclarative()` and `.WithOrigin("format=<tag-value>")`.
2. Error types `Required` and `NotSupported` are exempt from `.WithOrigin()`.
3. Do NOT mark `.MarkCoveredByDeclarative()` on uniqueness errors when using `+k8s:customUnique`.
4. **Operator precedence**: `append(allErrors, err).MarkCoveredByDeclarative()` marks ALL errors. Correct: `append(allErrors, err.MarkCoveredByDeclarative())`.
5. Avoid redundant validation structure (spec fields in both top-level validator and `validateSpec()`).

### Test Conventions
1. Use unified test case maps with `expectedErrs field.ErrorList` (nil = success). No separate loops.
2. Include boundary cases: for `+k8s:minimum=0`, test -1, 0, 1, and a larger positive value.
3. Use tweak functional options: `mkValid<Kind>(tweakers ...func(*api.<Kind>))`.
4. Set ResourceVersion in the test loop, not mk helpers.
5. Order API versions semantically: `v1alpha1`, `v1beta1`, `v1`.
6. Both create and update test coverage for every migrated field.
7. Accurate function/test names (e.g., `tweakEgressToIPBlock` not `tweakEgressIPBlock`).
8. No unnecessary linewraps.

### Ratcheting
- DV auto-ratchets unchanged fields on update, but HV may not. Verify behavior parity.
- If HV always validates regardless of old value, this creates a mismatch. Skip the tag or update HV.

### Fuzz Testing
- Register new API groups in `pkg/api/testing/validation_test.go` `typesWithDeclarativeValidation` slice.
- Shared internal types (e.g., `Scale`) across API groups may block registration until all groups are plumbed.

### Code Generation
- After completing code modifications, run `hack/update-codegen.sh validation`.
- Do NOT run `make verify`.
- Run tests: `make test WHAT=./pkg/registry/<group>/<kind>`.

## Reference PRs

When reviewing or authoring DV changes, these PRs serve as exemplars:

| PR | Description | Key Pattern |
| :--- | :--- | :--- |
| [#130724](https://github.com/kubernetes/kubernetes/pull/130724) | Enable DV for ReplicationController | Original exemplar, test structure |
| [#132361](https://github.com/kubernetes/kubernetes/pull/132361) | Enable DV for CertificateSigningRequest | Multi-version pattern |
| [#133068](https://github.com/kubernetes/kubernetes/pull/133068) | CSR/status subresource | Subresource pattern |
| [#134072](https://github.com/kubernetes/kubernetes/pull/134072) | Enable DV for ResourceClaim | Complex resource |
| [#134113](https://github.com/kubernetes/kubernetes/pull/134113) | ResourceClaim/status + centralized helper | Refactored helper pattern |
| [#133937](https://github.com/kubernetes/kubernetes/pull/133937) | Simplified testing | Simplified test framework |
| [#135412](https://github.com/kubernetes/kubernetes/pull/135412) | Enable DV for HorizontalPodAutoscaler | Feature-gated validation, shared types in fuzz |
| [#135438](https://github.com/kubernetes/kubernetes/pull/135438) | Storage group wiring | New API group plumbing, `+k8s:immutable` on struct |
| [#135761](https://github.com/kubernetes/kubernetes/pull/135761) | DV for ValidatingAdmissionPolicyBinding | MarkCoveredByDeclarative precedence pitfall |
| [#135763](https://github.com/kubernetes/kubernetes/pull/135763) | Wire admissionregistration for DV | API group plumbing, fuzz test registration |
| [#135951](https://github.com/kubernetes/kubernetes/pull/135951) | DV for CronJob Schedule | Ratcheting mismatch awareness, tag ordering |
| [#136793](https://github.com/kubernetes/kubernetes/pull/136793) | DV Framework: Validation Lifecycle | Alpha/Beta/GA lifecycle, `WithDeclarativeEnforcement()` option |

**For structured PR review workflows and test coverage analysis, use the `/dv:review` command.**
