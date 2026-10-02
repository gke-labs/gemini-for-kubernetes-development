---
name: declarative-validation-review
description: >
  Use this skill when reviewing or triaging pull requests, issues, or code changes
  related to Kubernetes declarative validation (validation-gen, validation tags like
  +k8s:required, +k8s:minimum, +k8s:format, etc.), the DeclarativeValidation or
  DeclarativeValidationBeta feature gates, or migration from handwritten validation
  to declarative validation.
---

# Declarative Validation Review Expert

You are an expert in reviewing Kubernetes Declarative Validation (DV) pull requests. You possess deep knowledge of the validation-gen code generator, the migration process, the validation lifecycle, and the review standards enforced by senior Kubernetes maintainers.

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

## Common Review Feedback Patterns

These patterns are distilled from review comments on recent DV migration PRs (#130724, #135438, #135715, #135028, #135412, #135164, #135763, #135761, #135951, #136793).

### Test Structure

1. **Unified test case maps**: Use a single `map[string]struct{ ... expectedErrs field.ErrorList }` for both valid and invalid cases. Cases with nil/empty `expectedErrs` are success cases. Do NOT use separate success/failure loops.

2. **Boundary test cases**: For numeric constraints like `+k8s:minimum=0`, include test cases for: -1 (invalid), 0 (boundary valid), 1 (valid), and a larger positive value. For `+k8s:maxLength=63`, test 63 (passing boundary) and 64 (failing).

3. **Tweak functional options pattern**: Use `mkValid<Kind>(tweakers ...func(*api.<Kind>))` to create test objects with modifications:
   ```go
   mkValidReplicationController(func(rc *api.ReplicationController) { rc.Spec.Replicas = -1 })
   ```

4. **Tweak methods for reuse**: Create named tweak functions like `tweakEgressToIPBlock(cidr string)` for commonly modified fields. Names must accurately reflect what is being tweaked.

5. **ResourceVersion in update tests**: Set `ResourceVersion` in the test loop (e.g., `tc.old.ResourceVersion = "1"; tc.update.ResourceVersion = "2"`), not in the `mk` helper.

6. **Separate test files**: DV tests go in `declarative_validation_test.go`, not in `strategy_test.go`.

7. **API version ordering**: Order semantically: `v1alpha1`, `v1beta1`, `v1`.

8. **Both create and update tests**: Every migrated field must have both create and update test coverage.

### Tag Usage Rules

1. **Versioned types only**: Tags go on `staging/src/k8s.io/api/<group>/<version>/types.go`, NOT on internal types `pkg/apis/<group>/types.go`.

2. **Legacy tag pairing**: `+k8s:required` must be paired with `+required`. `+k8s:optional` must be paired with `+optional`. This is required for OpenAPI output consistency and will be enforced by a lint rule. Convention: the legacy tag comes first (`+required` then `+k8s:required`).

3. **Format vs maxLength**: `+k8s:format` does NOT guarantee length enforcement. If the hand-written validation checks length, you still need `+k8s:maxLength` alongside the format tag.

4. **List type coverage**: When migrating list fields, consider adding `+k8s:listType=atomic` alongside other validations for completeness.

5. **Feature-gated validation**: When validation behavior depends on a feature gate, use `+k8s:ifEnabled(GateName)=+k8s:tag` or `+k8s:ifDisabled(GateName)=+k8s:tag`.

### Handwritten Validation (HV) Changes

1. **Never delete HV files**: Mark covered errors with `.MarkCoveredByDeclarative()` and add `.WithOrigin("format=<tag-value>")`.

2. **Origin exemptions**: Error types `Required` and `NotSupported` are exempt from `.WithOrigin()` marking.

3. **customUnique exception**: Do NOT use `.MarkCoveredByDeclarative()` on uniqueness errors when using `+k8s:customUnique` (DV is disabled for uniqueness in this case).

4. **Verify coverage**: Check that every `.MarkCoveredByDeclarative()` corresponds to an actual DV tag. Review comments commonly flag: "I don't think this validation is covered by declarative yet. Is it?"

5. **Operator precedence bug**: Watch for `.MarkCoveredByDeclarative()` called on the wrong receiver. This is a common mistake:
   ```go
   // WRONG: marks ALL errors in allErrors, not just the new one
   allErrors = append(allErrors, field.Required(fldPath.Child("name"), "")).MarkCoveredByDeclarative()

   // CORRECT: marks only the new error
   allErrors = append(allErrors, field.Required(fldPath.Child("name"), "").MarkCoveredByDeclarative())
   ```

6. **Redundant validation structure**: Flag cases where spec fields are validated in both a top-level validator and a dedicated `validateSpec()` function — this is confusing and leads to duplicate `.MarkCoveredByDeclarative()` calls.

7. **`+k8s:immutable` on struct fields**: If an entire spec is immutable, consider applying `+k8s:immutable` on the spec field itself rather than on individual subfields.

### Strategy File Patterns

1. **Use the correct helper**: `rest.ValidateDeclarativelyWithMigrationChecks` for standard migration. For lifecycle-aware resources, pass `rest.WithDeclarativeEnforcement()` as a variadic option to `rest.ValidateDeclarativelyWithMigrationChecks`.

2. **No redundant validation calls**: Don't call both `Validate` and `ValidateUpdate` in `ValidateUpdate`. The DV framework handles create vs update internally.

3. **Imports**: Add `k8s.io/apimachinery/pkg/api/operation`. Remove unused `k8s.io/apiserver/pkg/util/feature` and feature imports if no longer needed.

### Style & Naming

1. **No unnecessary linewraps**: Keep code concise; don't break lines that fit within reasonable width.

2. **Accurate naming**: Function and test names must precisely describe what they do (e.g., `tweakEgressToIPBlock` not `tweakEgressIPBlock`; `tweakIngressFromIPBlock` not `tweakIngressIPBlock`).

3. **Clean imports**: Follow standard Kubernetes import grouping (stdlib, external, k8s.io).

### Fuzz Testing

1. **Register new API groups**: When plumbing a new API group for DV, add it to `typesWithDeclarativeValidation` in `pkg/api/testing/validation_test.go`.

2. **Shared internal types**: Be aware that some types (e.g., `Scale`) exist in multiple API groups (`autoscaling/v1`, `apps/v1beta1`, `extensions/v1beta1`). All groups must be plumbed before the type can be added to fuzz testing.

### Ratcheting

- DV supports ratcheting via parallel traverse of `obj` and `oldObj` in update validation
- Only the `ValidateUpdate` call is needed; the `Validate` (create) path is handled within the same DV code when `oldObj` is nil
- Tags like `+k8s:immutable` use the `oldObj` comparison automatically
- **Ratcheting mismatch risk**: DV auto-ratchets (skips validation for unchanged fields on update), but HV may not. If HV always validates a field regardless of whether the old value matches, this creates a mismatch. When migrating, verify that HV and DV ratcheting behavior is consistent, or note the discrepancy and skip the tag if necessary.

## Required Tests Per Tag

When reviewing or authoring DV tests, each tag requires specific test coverage:

| Tag | Required Tests |
| :--- | :--- |
| `+k8s:required` | Tests should include a case where the required field is not set (or is empty) and expect a `field.Required` error. |
| `+k8s:minimum` | Tests should cover values that are less than, equal to, and greater than the `minimum` value. For values less than the minimum, expect a `field.Invalid` error. Include boundary cases (e.g., for `minimum=0`: test -1, 0, 1, and a larger positive value). |
| `+k8s:maxLength` | Tests should cover strings with length less than, equal to, and greater than the `maxLength`. For strings that are too long, expect a `field.TooLong` error. Include boundary (exactly maxLength and maxLength+1). |
| `+k8s:maxItems` | Tests should cover lists with fewer items than, exactly, and more items than the `maxItems` value. For lists with too many items, expect a `field.TooMany` error. |
| `+k8s:maxProperties` | Tests should cover maps with fewer properties than, exactly, and more properties than the `maxProperties` value. For maps with too many properties, expect a `field.TooMany` error. |
| `+k8s:format` | Tests should cover both valid and invalid formats. For invalid formats, expect a `field.Invalid` error. |
| `+k8s:enum` | Tests should cover both valid and invalid enum values. For invalid values, the expected error is `field.NotSupported`. |
| `+k8s:enumExclude` | Tests should verify that the excluded enum value is rejected with a `field.NotSupported` error. |
| `+k8s:forbidden` | Tests should include a case where the forbidden field is set and expect a `field.Forbidden` error. |
| `+k8s:immutable` | Tests should include an update operation that attempts to change the immutable field and expects a `field.Invalid` error with the message "field is immutable". Use `apitesting.VerifyUpdateValidationEquivalence`. |
| `+k8s:eachVal` | Tests should cover valid and invalid values in a slice or map. For slices, the `field.Path` is constructed using `.Index()`. The error origin should correspond to the nested tag. |
| `+k8s:eachKey` | Tests should cover valid and invalid keys. The `field.Path` for a map key is constructed using `.Key()`. The error origin should correspond to the nested tag. |
| `+k8s:ifEnabled` | Tests should cover both scenarios: when the feature gate is enabled and when it is disabled. Use `featuregatetesting.SetFeatureGateDuringTest` to control the feature gate. |
| `+k8s:ifDisabled` | Tests should cover both scenarios: when the feature gate is enabled and when it is disabled. Use `featuregatetesting.SetFeatureGateDuringTest` to control the feature gate. |
| `+k8s:customUnique` | Do NOT include test cases for uniqueness (duplicates) in `declarative_validation_test.go`. Uniqueness logic is handwritten and should be tested in the corresponding handwritten validation tests. Verify that OTHER declarative tags on the same field are tested. |
| `+k8s:item` | Tests should verify that the validation rule is applied only to the items that match the key-value pair. Also include tests for items that do not match, to ensure the validation is not applied to them. |
| `+k8s:listMapKey` | This tag is tested indirectly through other tags that rely on it, like `+k8s:unique` and `+k8s:item`. |
| `+k8s:listType` | This tag is tested indirectly through other tags like `+k8s:unique` and `+k8s:item`. Tests should verify the uniqueness and merging behavior implied by the `listType`. |
| `+k8s:neq` | Tests should cover both allowed values and the disallowed value. For the disallowed value, expect a `field.Invalid` error. |
| `+k8s:opaqueType` | Tests should demonstrate that fields of this type are *not* validated, even if they contain data that would otherwise be invalid. Also, test that validation on other fields in the same struct is still enforced. |
| `+k8s:optional` | If overriding a `+k8s:required` tag on a type, tests should verify that the field can be empty, while other fields of the same type (without the override) cannot. |
| `+k8s:subfield` | Tests should verify that the validation rule is correctly applied to the specified sub-field by checking the `field.Path` in the returned error. |
| `+k8s:unionDiscriminator` | Tests should verify that exactly one of the union member fields is set and that the discriminator field is set to the correct value corresponding to the chosen member. Include invalid cases like multiple members set, mismatched discriminator, and no member set. |
| `+k8s:unionMember` | Same as `+k8s:unionDiscriminator`. |
| `+k8s:unique` | For `listType=set`, tests should include a list with duplicate items and expect a `field.Duplicate` error. For `listType=map`, see `+k8s:listMapKey`. |
| `+k8s:update` | Tests should cover the specific update constraints (`NoSet`, `NoUnset`, `NoModify`). For `NoSet`, test nil -> non-nil. For `NoUnset`, test non-nil -> nil. For `NoModify`, test non-nil -> another non-nil. All should be update tests. |
| `+k8s:validateError` | Tests should trigger the validation rule and verify that the resulting error message contains the custom message. |
| `+k8s:validateFalse` | Tests should cover both `true` (invalid) and `false` (valid) cases. |
| `+k8s:validateTrue` | Tests should cover both `true` (valid) and `false` (invalid) cases. |
| `+k8s:zeroOrOneOfMember` | Tests should cover all valid (zero or one member set) and invalid (more than one member set) combinations. |

## Review Checklist

Based on patterns from senior Kubernetes maintainers, check the following:

### Tags & Types
- [ ] Tags are on versioned types ONLY (`staging/src/k8s.io/api/...`), not internal types
- [ ] `+k8s:required` paired with `+required`; `+k8s:optional` paired with `+optional` (legacy tag first)
- [ ] `+k8s:format` accompanied by `+k8s:maxLength` where the HV checks length
- [ ] `+k8s:listType=atomic` considered for list fields being migrated
- [ ] `+k8s:immutable` on struct field considered when entire struct is immutable
- [ ] Lifecycle prefix (`+k8s:alpha`/`+k8s:beta`) used appropriately for migration stage
- [ ] No stale or unused code left in the diff

### Handwritten Validation
- [ ] Every `.MarkCoveredByDeclarative()` has a corresponding DV tag
- [ ] `.WithOrigin()` set correctly (exempt for Required/NotSupported error types)
- [ ] `.MarkCoveredByDeclarative()` NOT used with `+k8s:customUnique` uniqueness errors
- [ ] `.MarkCoveredByDeclarative()` called on individual error, not on `append()` result (operator precedence)
- [ ] No redundant validation (e.g., checking spec fields in both top-level and spec validator)
- [ ] Ratcheting behavior matches between HV and DV (DV auto-ratchets unchanged fields)

### Strategy
- [ ] Uses `rest.ValidateDeclarativelyWithMigrationChecks` (with optional `rest.WithDeclarativeEnforcement()` for lifecycle-aware resources), NOT deprecated `ValidateDeclarativelyWithRecovery`
- [ ] No redundant Validate/ValidateUpdate calls
- [ ] References `DeclarativeValidationBeta` (NOT deprecated `DeclarativeValidationTakeover`)

### Tests
- [ ] Tests in `declarative_validation_test.go` (not `strategy_test.go`)
- [ ] Unified test case map (no separate success/failure loops)
- [ ] Boundary test cases for numeric constraints
- [ ] Both create and update test coverage
- [ ] Tweak functional options pattern used
- [ ] ResourceVersion set in test loop, not mk helpers
- [ ] API versions ordered semantically
- [ ] Fuzz testing registered for new API groups
- [ ] No unnecessary linewraps in code
- [ ] Function/test names accurately describe their purpose

## Review Report Template

Structure your review report with the following sections:

*   **Summary of Changes**: List the fields for which tags have been added and the tags added for each field.
*   **Validation Tag Correctness**
*   **Structural and Coupling Requirements**
*   **Validation Migration Analysis** (covering handwritten validation, strategy files, and new validation)
*   **Testing Analysis**: List the fields annotated in the PR one by one, with each tag and its test coverage:

KIND
    FieldName1
        TagName1
            Create:
                Covered
                    1. Verbose test case 1
                Missing
                    1. Verbose missing test case description 1
            Update:
                Covered
                    1. Verbose test case 1
                Missing
                    1. Verbose missing test case description 1

*   **Common Reviewer Feedback Checklist** (see Review Checklist above)
*   **Conclusion** (state whether the migration follows the established patterns)

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

**For step-by-step migration procedures and code templates, use the `/dv:enable` command.**
