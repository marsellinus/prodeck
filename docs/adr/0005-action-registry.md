# ADR-0005: Action registry with typed interfaces, no central switch

Status: accepted (Milestone 1)

## Context
The system will grow to dozens of actions across core, plugins, and user profiles. The
failure mode to avoid is a growing `switch actionType { ... }` in the dispatcher, which
makes every integration edit touch core code (explicitly forbidden by requirement §40).

## Decision
```go
type Action interface {
    Type() string
    Scope() auth.Scope
    Validate(params json.RawMessage) error
    Run(ctx context.Context, req Request) (Result, error)
}
```
Registered in `engine.Registry`. Dispatch is a map lookup; unknown type → `unsupported`.

## Consequences
- Adding an action = adding a file + one `Register` call. Plugins use the same interface
  as core, so the plugin system is not a special case.
- Scopes travel with the action, so the permission check is data, not a second switch.
- `Validate` is mandatory, so bad params are rejected before any side effect.
- Macro is just another `Action` whose steps are action objects → nesting, cancellation,
  and per-step timeout come for free and every future action works inside a macro.
