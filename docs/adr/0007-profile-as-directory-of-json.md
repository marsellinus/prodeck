# ADR-0007: A profile is a directory of JSON, host-authoritative

Status: accepted (Milestone 1)

## Context
Layouts must be editable, exportable, importable, diffable, and git-friendly
(requirement §24), while the phone must render them with no app update (§40).

## Decision
`profiles/<id>/profile.json` (+ `icons/`) is the single source of truth. The host
validates it on load and hot-reloads on change; the client fetches it, caches it, and
renders it. The client never owns authoritative layout state.

## Consequences
- `git diff` shows exactly what changed in a layout; a profile can be reviewed like code.
- Adding a button, page, or action type requires no Android release.
- Cost: a malformed profile is a runtime condition, so validation must be strict and
  must name the offending JSON pointer, and an invalid profile must never replace a
  working one (documented in ARCHITECTURE.md §6).
- Cost: no server-side UI. Accepted for M1; the visual editor (phase 4) will edit these
  same files through the admin API rather than introducing a second storage format.
- Rejected: SQLite (opaque, not diffable, not git-friendly for the stated workflow).
