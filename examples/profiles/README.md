# Example profiles

The canonical, maintained example is **`profiles/development/profile.json`** in
the repository root — not a copy here. It is the profile the project's own
screenshots and tests are taken from, and it is the one to read first.

This directory exists so that the path `examples/profiles/` is where people
look. It deliberately holds no second copy of the example: two profiles that
drift apart are worse than none, and a profile is meant to be read, diffed, and
copied.

---

## Anatomy of a profile

A profile is a **directory**, not a single file (ADR-0007):

```
profiles/
└── development/
    ├── profile.json     the layout: schema, theme, settings, pages, buttons
    └── icons/           optional image icons referenced by file name
        └── terminal.png
```

`profile.json` is the single source of truth. The host validates it on load and
hot-reloads it on change; the client fetches it, caches it, and renders it. The
client holds no authoritative layout state, which is what makes hand-editing
safe and what lets you add a button without an Android release.

The full document reference is `docs/PROTOCOL.md` §5. The short version:

| Field | Meaning |
|-------|---------|
| `schema` | Document schema version. Currently `1`. |
| `id` | Must equal the directory name. |
| `name`, `icon` | What the client shows in the profile list. |
| `theme` | Colours, radius, font scale, spacing, animation. |
| `settings` | `grid` (columns/rows), `haptic`, `nav`, and optional `auto_return`. |
| `root_page` | The page id shown first. |
| `pages[]` | Each with `id`, `name`, optional `grid`, and `buttons[]`. |
| `pages[].buttons[]` | `id`, `label`, `icon`, `cell`, `state`, and the `on_press` / `on_long_press` / `on_release` actions. |

Validation is strict and total: unknown fields are rejected, grid bounds are
enforced (columns/rows ≤ 16, at most 512 buttons per page), every action type
must be registered on the host, and an invalid profile is never served — the
error names the offending JSON pointer, and the previously working profile keeps
running. See `docs/ARCHITECTURE.md` §6.

### Icons

Three forms are in use:

```json
"icon": { "type": "emoji",    "value": "🖥️" }
"icon": { "type": "material", "value": "play_arrow" }
"icon": { "type": "image",    "value": "terminal.png" }
```

An `image` icon's `value` is a file name resolved inside the profile's own
`icons/` directory. The default profile uses `emoji` and `material` icons; the
placeholder PNGs under `profiles/development/icons/` are the format reference
for the `image` form and can be regenerated with:

```sh
python3 scripts/gen-icons.py
```

---

## Import and export

Profiles move between hosts as JSON, and the wire protocol has both directions
(PROTOCOL.md §5.3):

- `profile.export` returns the document as a JSON string. Requires nothing
  beyond a valid session.
- `profile.import` takes `{ "json": "...", "overwrite": false }` and requires
  the **`profiles.write`** scope. A `conflict` error means the profile id
  already exists and `overwrite` was false.

At the file level, import/export is just copying the directory:

```sh
# export from this host
cp -r ~/.config/mobiledeck/profiles/development ./development-export

# import onto another host
cp -r ./development-export ~/.config/mobiledeck/profiles/development
```

The host picks it up on `profile.reload` or restart.

---

## Committing a profile to git

Profiles are designed to be reviewed like code. The layout is chosen for it:

- One JSON document per profile, one directory per profile.
- Buttons are stable, ordered objects with explicit ids, so `git diff` shows the
  button that changed rather than a reshuffled blob.
- No opaque database, no binary container, no server-side state that the diff
  cannot show (ADR-0007 explicitly rejects SQLite for this reason).

Recommended practice:

1. Track `profiles/` and nothing else from the config directory. `devices.json`,
   `audit.jsonl`, `config.json`, `logs/`, and `tls/` are machine-local secrets
   and are gitignored by the shipped `.gitignore` (SECURITY.md §6).
2. Keep the profile id equal to its directory name; the host enforces it.
3. Commit the `icons/` directory alongside `profile.json` so the profile is
   self-contained and a clone renders identically.
4. Treat a profile as code-equivalent: it can launch processes and inject input,
   which is why it is reviewed and why `require_confirmation` exists for
   destructive action types (SECURITY.md T10).
5. Prefer one profile per use case with a descriptive `id` (`development`,
   `streaming`, `music`) over one large profile with many pages, unless the
   pages genuinely belong together.

Example:

```sh
git add profiles/development/profile.json profiles/development/icons/
git commit -m "profiles: add a docker page to development

Two buttons for the docker plugin and a counter tile for the running
container count."
```

---

## Related reading

- `docs/PROTOCOL.md` §5 — the profile document, action objects, button states.
- `docs/ARCHITECTURE.md` §5 — where profiles live on disk.
- `docs/adr/0007-profile-as-directory-of-json.md` — why a directory of JSON.
- `docs/DEVELOPMENT.md` §7 — writing a profile by hand.
- `docs/PLUGIN_DEVELOPMENT.md` — plugin-provided action types.
