# Plugin development

Status: **interface frozen; runtime is Milestone 3 (not implemented).**

The `Action` interface below is final and will not change without a protocol
version bump. The host does not load plugins yet: `plugins/` is parsed only
once the Milestone 3 loader lands. Until then a plugin directory is inert, and
a profile referencing `<plugin>.<verb>` fails validation with `unsupported`.
Write against this document now; the code you produce will not need changes.

---

## 1. What a plugin is

A plugin is a directory under `plugins/` that declares one or more actions in
the same namespace and, optionally, emits events and reads settings. It is
**not** a separate process in M3: a plugin is a Go package compiled into the
host, or a binary the host starts and speaks the plugin protocol to. Both cases
use the same manifest and the same `Action` interface.

A plugin MUST NOT be a `switch` over action names inside core. If adding a
plugin requires editing a file under `host/internal/engine` or
`host/internal/server`, the design is wrong (ADR-0005, ARCHITECTURE.md §3.1).

---

## 2. The `Action` interface

Declared in `host/internal/engine`:

```go
type Action interface {
    // Type is the namespaced identifier, e.g. "docker.start".
    Type() string
    // Scope is the permission a device must hold before Run is called.
    Scope() auth.Scope
    // Validate checks parameters before anything runs. Pure, no side effects.
    Validate(params json.RawMessage) error
    // Run performs the action. Must honour ctx cancellation.
    Run(ctx context.Context, req Request) (Result, error)
}
```

`Request` carries the caller's context and is deliberately flat, so an action
cannot reach back into a session:

```go
type Request struct {
    ExecutionID string
    DeviceID    string
    ProfileID   string
    PageID      string
    ButtonID    string
    ActionType  string
    Params      json.RawMessage
    Scopes      auth.ScopeSet
    Depth       int // macro nesting level
}
```

`Result` is returned to the client as `action.result.output`:

```go
type Result struct {
    Output any    `json:"output,omitempty"`
    Detail string `json:"detail,omitempty"`
}
```

Contract rules enforced by the host, which a plugin must satisfy:

- `Type()` is unique across core and all plugins. Registering a duplicate is a
  start-up error, never "last one wins".
- `Scope()` travels with the action. The engine checks it **before** `Run`; a
  plugin never performs its own permission check.
- `Validate` is called before `Run`, always. A plugin that mutates state in
  `Validate` is a bug.
- `Run` MUST honour `ctx.Done()` and return promptly. The engine cancels
  executions, and `action.cancel` from the client reaches you as a cancelled
  context.
- Errors map to protocol codes through `engine.ErrorCode`; returning
  `platform.ErrUnsupported` yields `unsupported`, a plain error yields
  `action_failed`.

### 2.1 Scopes

Use the narrowest scope that fits (PROTOCOL.md §9):

| Scope | Use for |
|-------|---------|
| `plugins` | Plugin-provided namespaces that are not otherwise covered. |
| `apps` | Launching applications, opening URLs and folders. |
| `scripts` | Running a command or script the plugin supplies. |
| `system.read` | Read-only telemetry. |
| `system.power` | Shutdown, restart, sleep, lock. |

`plugins` is granted only when the host operator includes it; it is not in
`auth.DefaultScopes()`. High-risk scopes (`scripts`, `system.power`) are never
granted by default. An action that needs one must be declared with that scope
and the profile button must list the matching entry in its `permissions` array.

---

## 3. Plugin manifest

Every plugin directory contains `plugin.json`:

```json
{
  "name": "docker",
  "version": "1.0.0",
  "api": 1,
  "description": "Control Docker containers from the deck.",
  "author": "Jane Doe <jane@example.com>",
  "license": "Apache-2.0",
  "actions": [
    {
      "type": "docker.containers",
      "description": "Push the running container count as button state.",
      "params": { "type": "object", "properties": {} }
    },
    {
      "type": "docker.start",
      "description": "Start a container.",
      "params": {
        "type": "object",
        "properties": { "name": { "type": "string" } },
        "required": ["name"]
      }
    },
    {
      "type": "docker.stop",
      "description": "Stop a container.",
      "params": {
        "type": "object",
        "properties": { "name": { "type": "string" } },
        "required": ["name"]
      }
    }
  ],
  "events": ["docker.containers"],
  "settings": [
    { "key": "socket", "type": "string", "default": "/var/run/docker.sock",
      "description": "Docker daemon socket or DOCKER_HOST." }
  ],
  "permissions": ["plugins"],
  "entrypoint": "docker-plugin"
}
```

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Directory name; must equal the namespace prefix of every action. |
| `version` | yes | Semver of the plugin. |
| `api` | yes | Plugin API version. Currently `1`. |
| `description` | yes | One line, shown by `mobiledeck plugins` (M3). |
| `author` | yes | Free text; an email is recommended for a third-party plugin. |
| `license` | yes | SPDX identifier. Must be Apache-2.0-compatible (no GPL/AGPL). |
| `actions` | yes | At least one. `type` must start with `<name>.`. |
| `events` | no | Event names this plugin emits, for client subscription. |
| `settings` | no | Operator-editable keys, stored per plugin. |
| `permissions` | no | Scopes the plugin as a whole requires, a union of its actions'. |
| `entrypoint` | no | Binary name for an out-of-process plugin. Omitted for a compiled-in plugin. |

Unknown fields in `plugin.json` are an error, matching the profile and config
loader rule: a typo must fail loudly, not silently disable a setting.

---

## 4. Directory layout

```
plugins/
└── docker/
    ├── plugin.json
    ├── main.go          # compiled-in plugin, package docker
    └── README.md        # optional
```

For an out-of-process plugin, `main.go` is replaced by a built binary named by
`entrypoint`; the plugin still ships `plugin.json` next to it. The host resolves
`plugins/` relative to the config root (`store.Paths`), so a user installs a
plugin by copying a directory into `~/.config/mobiledeck/plugins/`.

---

## 5. Registration

A compiled-in plugin registers in an `init` or an explicit `Register` call that
the host makes at start-up:

```go
func Register(reg *engine.Registry) error {
    for _, a := range []engine.Action{
        containersAction{}, startAction{}, stopAction{},
    } {
        if err := reg.Register(a); err != nil {
            return err
        }
    }
    return nil
}
```

`Registry.Register` rejects a duplicate type. `Registry.Lookup` first checks
core actions, then the plugin registry, so a plugin can never shadow a core
action. Adding a plugin means adding a directory and one `Register` call — no
core file changes, no central dispatch table.

---

## 6. Emitting `event.button.state`

A plugin that produces a value for a tile calls `Engine.SetButtonState`:

```go
e.SetButtonState(engine.ButtonState{
    ProfileID: "development",
    PageID:    "home",
    ButtonID:  "docker",
    Type:      "counter",
    Value:     ptr(float64(running)),
    Label:     fmt.Sprintf("%d Running", running),
    Color:     "#22c55e",
})
```

`SetButtonState` is a no-op when the value is unchanged and coalesces bursts to
at most 10 Hz per button (PROTOCOL.md §6.4). The client applies the override to
the tile identified by `(profile_id, page_id, button_id)`; the profile document
declares the button with a matching `state.type`.

Push state only when it changes. A plugin that polls must do so on a ticker and
cancel the ticker when its context ends; it MUST NOT hold a goroutine per
button forever.

---

## 7. Sandboxing, panics, and failure

- **Panic recovery.** A panic inside `Validate` or `Run` is recovered at the
  plugin boundary. The plugin is marked unhealthy, the host stays up, and the
  client receives `action_failed` (ARCHITECTURE.md §6). A plugin must not rely
  on this: return errors instead.
- **No privilege escalation.** The plugin runs with the host's privileges, as
  the logged-in user. It never elevates (SECURITY.md §4).
- **Confinement.** Path arguments are confined to the profile directory and the
  host `scripts/` directory unless `--allow-absolute-paths` was passed. A plugin
  that resolves paths itself must apply `platform.ConfinePath` for the same
  reason.
- **Bounded resources.** Actions run on the engine's worker pool
  (`engine.max_concurrent_actions`, a config field, default 8). A plugin must not
  spawn unbounded goroutines or block indefinitely; the per-action timeout applies
  to it like any core action.
- **Out-of-process plugin.** Started with `platform.CommandContext` so its
  process group is killed on cancel or timeout, and so it cannot outlive the
  host.

---

## 8. API versioning

`api: 1` is the current plugin API. It covers the `Action` interface, the
manifest schema, the `Request`/`Result` types, and `SetButtonState`.

- Additive changes (a new optional manifest field, a new scope, a new event)
  do **not** bump `api`.
- Removing a field, changing a field's type, or changing the meaning of an
  existing field **does** bump `api`.
- The host refuses to load a plugin whose `api` it does not implement, with a
  clear error naming both versions. It does not attempt best-effort loading.
- The interface in §2 is frozen for `api: 1`.

---

## 9. Worked example: the `docker` plugin

`docker.containers` pushes a counter state; `docker.start` and `docker.stop`
start and stop a named container. It shells out to the `docker` CLI through
`platform.Shell` so the host keeps one process-spawning path.

### 9.1 `plugins/docker/plugin.json`

```json
{
  "name": "docker",
  "version": "1.0.0",
  "api": 1,
  "description": "Control Docker containers from the deck.",
  "author": "MobileDeck contributors",
  "license": "Apache-2.0",
  "actions": [
    { "type": "docker.containers", "description": "Count running containers." },
    { "type": "docker.start", "description": "Start a container." },
    { "type": "docker.stop", "description": "Stop a container." }
  ],
  "events": ["docker.containers"],
  "settings": [
    { "key": "command", "type": "string", "default": "docker" }
  ],
  "permissions": ["plugins"],
  "entrypoint": ""
}
```

### 9.2 `plugins/docker/main.go`

```go
// Package docker is a MobileDeck plugin. Every action is an engine.Action and
// is looked up by its Type() string; no switch over action names exists.
package docker

import (
    "context"
    "encoding/json"
    "fmt"
    "strconv"
    "strings"
    "time"

    "github.com/mobiledeck/mobiledeck/host/internal/auth"
    "github.com/mobiledeck/mobiledeck/host/internal/engine"
    "github.com/mobiledeck/mobiledeck/host/internal/platform"
)

// p is the plugin instance, shared by its actions. The host constructs it once
// and hands it the platform adapters and the button-state sink.
type p struct {
    plat    *platform.Platform
    command string
    emit    func(engine.ButtonState)
}

// ContainersAction counts running containers and pushes the count as state.
type ContainersAction struct{ p *p }

func (ContainersAction) Type() string      { return "docker.containers" }
func (ContainersAction) Scope() auth.Scope { return auth.ScopePlugins }

// Validate is pure: it decodes and checks parameters, and touches nothing.
func (ContainersAction) Validate(json.RawMessage) error { return nil }

func (a ContainersAction) Run(ctx context.Context, req engine.Request) (engine.Result, error) {
    res, err := a.p.plat.Shell.RunCommand(ctx, platform.CommandOptions{
        Command: a.p.command + " ps -q",
        Shell:   "auto",
        Timeout: 10 * time.Second,
    })
    if err != nil {
        return engine.Result{}, err
    }
    running := len(strings.Fields(res.Stdout))

    // The tile is declared with state.type "counter" in the profile; the host
    // only forwards this to the client when the value changed.
    if a.p.emit != nil {
        value := float64(running)
        a.p.emit(engine.ButtonState{
            ProfileID: req.ProfileID,
            PageID:    req.PageID,
            ButtonID:  req.ButtonID,
            Type:      "counter",
            Label:     strconv.Itoa(running) + " Running",
            Value:     &value,
        })
    }
    return engine.Result{Output: map[string]any{"running": running}}, nil
}

// nameParams is the parameter shape shared by docker.start and docker.stop.
type nameParams struct {
    Name string `json:"name"`
}

// decodeParams mirrors the engine's strict decoding: an unknown field is a typo
// in a profile, so it must be an error rather than a silently ignored setting.
func decodeParams(raw json.RawMessage, v any) error {
    if len(raw) == 0 {
        raw = json.RawMessage("{}")
    }
    dec := json.NewDecoder(strings.NewReader(string(raw)))
    dec.DisallowUnknownFields()
    return dec.Decode(v)
}

// StartAction starts a container.
type StartAction struct{ p *p }

func (StartAction) Type() string      { return "docker.start" }
func (StartAction) Scope() auth.Scope { return auth.ScopePlugins }

func (StartAction) Validate(params json.RawMessage) error {
    var in nameParams
    if err := decodeParams(params, &in); err != nil {
        return err
    }
    if strings.TrimSpace(in.Name) == "" {
        return fmt.Errorf(`docker.start: "name" must not be empty`)
    }
    return nil
}

func (a StartAction) Run(ctx context.Context, req engine.Request) (engine.Result, error) {
    var in nameParams
    if err := decodeParams(req.Params, &in); err != nil {
        return engine.Result{}, err
    }
    // argv, not a shell string: the container name is never interpolated into a
    // command line the host builds.
    res, err := a.p.plat.Shell.RunCommand(ctx, platform.CommandOptions{
        Command: a.p.command + " start " + in.Name,
        Shell:   "auto",
        Timeout: 30 * time.Second,
    })
    if err != nil {
        return engine.Result{}, err
    }
    return engine.Result{Output: res}, nil
}

// StopAction stops a container.
type StopAction struct{ p *p }

func (StopAction) Type() string      { return "docker.stop" }
func (StopAction) Scope() auth.Scope { return auth.ScopePlugins }

func (StopAction) Validate(params json.RawMessage) error {
    var in nameParams
    if err := decodeParams(params, &in); err != nil {
        return err
    }
    if strings.TrimSpace(in.Name) == "" {
        return fmt.Errorf(`docker.stop: "name" must not be empty`)
    }
    return nil
}

func (a StopAction) Run(ctx context.Context, req engine.Request) (engine.Result, error) {
    var in nameParams
    if err := decodeParams(req.Params, &in); err != nil {
        return engine.Result{}, err
    }
    res, err := a.p.plat.Shell.RunCommand(ctx, platform.CommandOptions{
        Command: a.p.command + " stop " + in.Name,
        Shell:   "auto",
        Timeout: 30 * time.Second,
    })
    if err != nil {
        return engine.Result{}, err
    }
    return engine.Result{Output: res}, nil
}

// Register is the plugin's single entry point. The host calls it once at
// start-up with the registry, the platform adapters, and the button-state sink.
// Adding an action is adding it to this slice; no core file changes.
func Register(reg *engine.Registry, plat *platform.Platform, emit func(engine.ButtonState)) error {
    inst := &p{plat: plat, command: "docker", emit: emit}
    for _, a := range []engine.Action{
        ContainersAction{p: inst},
        StartAction{p: inst},
        StopAction{p: inst},
    } {
        if err := reg.Register(a); err != nil {
            return err
        }
    }
    return nil
}
```

Notes on the code:

- Every action declares its own `Type()` and `Scope()`. There is no shared
  `switch`, and no action name is compared anywhere.
- `Validate` is pure and rejects unknown fields, matching the engine's own
  strict parameter decoding.
- Container names are passed as an argv element, never quoted into a shell
  string. If a value genuinely needs shell quoting, use `run_script`-style argv
  execution rather than building a command line.
- The state push carries `Type: "counter"`, which must match the button's
  `state.type` in the profile. Profile validation checks this once the plugin is
  loaded.

### 9.3 Profile that uses it

`profiles/development/pages/docker.json` (or inline in `profile.json`):

```json
{
  "id": "docker",
  "name": "Docker",
  "grid": { "columns": 4, "rows": 5 },
  "buttons": [
    {
      "id": "docker",
      "label": "Docker",
      "icon": { "type": "emoji", "value": "\ud83d\udc33" },
      "cell": { "row": 0, "column": 0, "row_span": 1, "column_span": 1 },
      "state": { "type": "counter", "default": "0 Running" },
      "on_press": { "type": "docker.containers", "params": {} },
      "permissions": ["plugins"]
    },
    {
      "id": "start",
      "label": "Start",
      "icon": { "type": "material", "value": "play_arrow" },
      "cell": { "row": 0, "column": 1, "row_span": 1, "column_span": 1 },
      "state": { "type": "momentary" },
      "on_press": { "type": "docker.start", "params": { "name": "postgres" } },
      "permissions": ["plugins"]
    },
    {
      "id": "stop",
      "label": "Stop",
      "icon": { "type": "material", "value": "stop" },
      "cell": { "row": 0, "column": 2, "row_span": 1, "column_span": 1 },
      "state": { "type": "momentary" },
      "on_press": { "type": "docker.stop", "params": { "name": "postgres" } },
      "permissions": ["plugins"]
    }
  ]
}
```

The button's `state.type` is `counter`, so the client renders
`event.button.state` value directly. The plugin pushes it with
`SetButtonState(... Type: "counter" ...)`; the two must agree, which profile
validation checks once the plugin is loaded.

### 9.4 Checklist before publishing

- [ ] `plugin.json` parses, `name` matches the directory and every action
      prefix, `api` is `1`.
- [ ] Every action type is unique and none shadows a core action
      (`Registry.Register` rejects both).
- [ ] `Validate` is pure and rejects unknown fields.
- [ ] `Run` returns on `ctx.Done()`.
- [ ] Scope is the narrowest that works; high-risk scopes are declared.
- [ ] The plugin is Apache-2.0-compatible and its license is in
      `THIRD_PARTY_LICENSES.md` if it vendors anything.
- [ ] No `switch` over action names was added to core.
