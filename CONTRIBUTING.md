# Contributing

Thanks for helping. This document is short on purpose: it lists the rules that
are actually enforced at review, and the commands a pull request is expected to
have run.

---

## 1. Getting started

```sh
git clone https://github.com/mobiledeck/mobiledeck
cd mobiledeck
```

Build and run instructions are in `docs/DEVELOPMENT.md`. Read
`docs/ARCHITECTURE.md` §2 before your first change; it is four paragraphs and it
is the document most reviews quote.

---

## 2. Architectural rules enforced at review

A change that breaks one of these is sent back, regardless of how well it works.

1. **Layering.** Dependencies flow one way:
   `cli → server → engine → platform`, with `auth`, `profile`, `telemetry`, and
   `store` feeding in from the side, and `proto` at the bottom depending on
   nothing. `internal/proto` performs no I/O and holds no global state.
   `internal/profile` performs no I/O except `Load`/`Save` through an injected
   `fs.FS`. `internal/server` owns no domain logic. `internal/cli` parses
   arguments and manages the process lifecycle and nothing else.

2. **No OS code outside `internal/platform`.** `runtime.GOOS` switches,
   `syscall`, `golang.org/x/sys`, `os/exec`, and per-OS libraries belong in
   `internal/platform`, behind build tags, implementing the interfaces in
   `platform/api.go`. `internal/engine` never imports an OS package and never
   touches `os/exec` directly — that is `platform.Shell`. The payoff is real:
   porting to a new OS means adding files under `internal/platform/` and
   changing nothing else.

3. **No central action switch.** Actions are types registered in
   `engine.Registry`; dispatch is a map lookup. There is no `switch` over action
   names anywhere in the host, and adding an action must not touch
   `internal/server` or a dispatcher (ADR-0005, ARCHITECTURE.md §3.1). A plugin
   uses the same `Action` interface as a core action; plugins are not a special
   case.

4. **No cloud dependency.** The host works on a machine with no internet access.
   Pairing, discovery, profiles, and every action are LAN-local. Cloud sync is
   an optional Phase 5 feature and must never become a prerequisite for anything.

5. **Profiles are git-friendly.** A profile is a directory of JSON
   (`profiles/<id>/profile.json` plus `icons/`), host-authoritative, validated on
   load, hot-reloaded on change (ADR-0007). No second storage format, no opaque
   database, no server-side state that a `git diff` cannot show. Adding a button
   or a page must never require an Android release.

6. **No Docker requirement.** The agent runs via `./mobiledeck run` on a bare
   machine. Docker appears only in `docker-compose.yml` for documentation and
   developer tooling (ADR-0009).

7. **Errors are honest.** An operation the OS cannot perform returns
   `platform.ErrUnsupported`; it never returns `nil` for work that did not
   happen. A peer that does not know a message type answers
   `error{code:"unsupported"}`; it never guesses (PROTOCOL.md §1).

8. **Security controls are not weakened silently.** TLS stays on for LAN binds,
   path confinement stays on, high-risk scopes stay off by default. A change
   that relaxes one must be opt-in, documented, and justified in the PR.

---

## 3. Commit messages

```
engine: bound macro recursion depth

A profile could nest macro inside macro until the stack was exhausted, which
turned a hand-edited typo into a host crash. The depth is now capped at the same
value the profile validator uses, so load-time and run-time agree.
```

- **Subject**: imperative mood, no trailing period, **at most 72 characters**.
  Prefix with the package or area (`engine:`, `platform:`, `android:`, `docs:`).
- **Body**: explain *why*, not what. The diff already shows what changed. If the
  change fixes a bug, say what the bug was and how it manifested. Wrap at 72
  columns.
- One logical change per commit. Do not mix a refactor with a behaviour change;
  a reviewer cannot tell them apart.
- Reference the issue or ADR when one exists.

---

## 4. Before opening a pull request

Run these, and say in the PR description that you ran them:

```sh
cd host && go vet ./... && go test ./...
cd android && ./gradlew assembleDebug
```

`go vet ./...` must be clean. `go test ./...` must pass. `./gradlew assembleDebug`
must produce `app/build/outputs/apk/debug/app-debug.apk`.

Also:

- Run `gofmt` (or `go fmt ./...`) on Go changes. Formatting is not a review
  topic.
- If you changed behaviour a documented test covers
  (`docs/SECURITY.md` §9), update or extend that test.
- If you changed the protocol, update `docs/PROTOCOL.md` and note in the PR
  whether the change is additive (no version bump) or breaking (version bump).
- If you added a dependency, add it to `THIRD_PARTY_LICENSES.md` with its
  license, and confirm it is not GPL/AGPL.

---

## 5. How to add an action

1. Create a file in the package that owns the namespace (or a plugin package for
   a plugin action). Implement `engine.Action`:

   ```go
   type StartAction struct{ base }

   func (StartAction) Validate(params json.RawMessage) error { /* strict decode */ }
   func (StartAction) Run(ctx context.Context, req engine.Request) (engine.Result, error) { /* honour ctx */ }
   ```

   Embed `base` (or call the constructors) to supply `Type()` and `Scope()`
   rather than repeating them.

2. Register it once at start-up with `registry.Register(a)`. Registration
   rejects a duplicate type; do not work around that, it means two packages
   claim the same type.

3. `Validate` must be pure and must reject unknown fields — a typo in a profile
   is an error, not a silently ignored parameter.

4. `Run` must return promptly on `ctx.Done()`. If it starts a process, start it
   with `platform.CommandContext` so it is killed as a process group.

5. Declare the narrowest scope that works. If it needs `scripts` or
   `system.power`, say so in the PR and expect the question "can it be
   narrower?".

6. Document the type in `docs/PROTOCOL.md` §10 and, for plugin actions, in
   `docs/PLUGIN_DEVELOPMENT.md`.

7. Test `Validate` rejection and the happy path. Do not test the registry
   wiring.

Do not add a case to a dispatch function. There is no dispatch function.

---

## 6. How to add an ADR

Architecture decision records live in `docs/adr/`, one file per decision, named
`NNNN-short-slug.md`.

1. Copy an existing file; increment the number; keep the format:
   `Context`, `Decision`, `Consequences`. Consequences must include the **cost**
   and the **rejected alternatives** — an ADR that lists only benefits is not
   finished.
2. Add a row to the table in `docs/adr/README.md`.
3. Set the status line. Accepted records are never rewritten: to change a
   decision, write a new ADR and mark the old one superseded.
4. Add an ADR when a decision is expensive to reverse: a dependency, a storage
   format, a protocol change, a security control, a license. Do not add one for
   a naming choice.

---

## 7. Review checklist

Reviewers work through this list. Authors can use it as a self-check.

- [ ] Does the dependency direction in §2 still hold? No OS code leaked out of
      `internal/platform`, no I/O added to `internal/proto` or
      `internal/profile`.
- [ ] Is there a new `switch` over action names, or a new branch in the server
      that knows about a specific action? (Reject.)
- [ ] Are unknown JSON fields rejected at the boundary, and do errors name the
      offending pointer or parameter?
- [ ] Does every operation that did not happen report it, rather than returning
      success?
- [ ] Are child processes started with `CommandContext` and killed as a group?
- [ ] Are scopes checked before execution, with the narrowest scope that works?
- [ ] Are secrets kept out of logs and out of `git`? (`devices.json`,
      `audit.jsonl`, tokens, PINs.)
- [ ] Is the protocol change additive? If breaking, does `v` bump and does
      `PROTOCOL.md` say so?
- [ ] Are profiles still plain JSON that `git diff` renders usefully?
- [ ] Do the tests assert observable behaviour (boundaries, errors, invariants)
      rather than that a function was called?
- [ ] Is documentation updated — `PROTOCOL.md` for the wire, `ARCHITECTURE.md`
      for structure, `ROADMAP.md` when a phase deliverable lands,
      `THIRD_PARTY_LICENSES.md` for a new dependency?
- [ ] Do commit subjects follow §3?

---

## 8. License and DCO

MobileDeck is licensed under the **Apache License 2.0** (ADR-0010, `LICENSE`).

- **No CLA.** There is no contributor license agreement to sign.
- **No DCO sign-off requirement.** There is no `Signed-off-by` obligation.
- By submitting a contribution you agree that it is licensed under the same
  Apache-2.0 terms as the project, and you confirm you have the right to submit
  it.
- Do not contribute code you did not write unless its license is compatible with
  Apache-2.0 and it is attributed in `THIRD_PARTY_LICENSES.md`.
- **GPL and AGPL dependencies are not accepted.** They are incompatible with the
  project's license and with the intent that plugins and commercial forks are
  permitted.
