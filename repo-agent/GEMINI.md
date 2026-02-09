# Development Guide

## CLI Commands

To ensure reusability and clean architecture, the logic behind CLI commands should reside in the `@pkg/commands/` directory.  Each should follow the options pattern, where there is a `RunFoo(ctx context.Context, opt FooOptions) error` function implementing the main logic, there is a `type FooOptions struct` which contains the options (and has an InitDefaults) method, and then there is a `BuildFooCommand() *cobra.Command` function
that builds the cobra.Command and binds the FooOptions fields to flags.

## Libraries

Avoid adding new dependencies on libraries that are not already in go.mod.

Prefer `sigs.k8s.io/yaml` for yaml.

## Verification

After making code changes, run the following commands to verify your changes and fix any errors:

*   **Linting:** `cd repo-agent ; make lint-go`
*   **Build:** `cd repo-agent; make build`