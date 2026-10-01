package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mobiledeck/mobiledeck/host/internal/store"
)

// seedExampleProfile writes the example profile on first run, so a new user has
// something to press before they have written a layout of their own.
//
// The profile is embedded here rather than shipped as a data file so the binary
// stays self-contained (docs/adr/0009-single-binary-no-docker-dependency.md).
// It is written only when the profiles directory has no profiles at all, so it
// can never overwrite a user's work.
func seedExampleProfile(profilesDir string) (bool, error) {
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(profilesDir, 0o700); err != nil {
				return false, fmt.Errorf("cli: create %s: %w", profilesDir, err)
			}
		} else {
			return false, fmt.Errorf("cli: read %s: %w", profilesDir, err)
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			// Something is already there. Never overwrite it.
			return false, nil
		}
	}

	dir := filepath.Join(profilesDir, "development")
	if err := os.MkdirAll(filepath.Join(dir, "icons"), 0o700); err != nil {
		return false, fmt.Errorf("cli: create %s: %w", dir, err)
	}
	if err := store.WriteFileAtomic(filepath.Join(dir, "profile.json"), []byte(ExampleProfile), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// ExampleProfile is the starter layout: a Development profile with the actions
// a first-time user is most likely to want, spread across three pages so the
// folder navigation is exercised too.
//
// Every action type it references exists in the core registry, so the profile
// loads on a fresh install with no plugins.
const ExampleProfile = `{
  "schema": 1,
  "id": "development",
  "name": "Development",
  "icon": { "type": "emoji", "value": "💻" },
  "theme": {
    "mode": "dark",
    "background": "#0e0e12",
    "accent": "#4c8bf5",
    "button_background": "#1c1c22",
    "button_foreground": "#f2f2f5",
    "button_radius": 14,
    "font_scale": 1.0,
    "spacing": 8,
    "animation": "fade"
  },
  "settings": {
    "grid": { "columns": 4, "rows": 4 },
    "haptic": true,
    "nav": { "breadcrumb": true, "back_button": true, "page_tabs": true }
  },
  "root_page": "home",
  "pages": [
    {
      "id": "home",
      "name": "Home",
      "buttons": [
        {
          "id": "copy",
          "label": "Copy",
          "icon": { "type": "material", "value": "content_copy" },
          "cell": { "row": 0, "column": 0 },
          "state": { "type": "momentary" },
          "on_press": { "type": "keyboard.shortcut", "params": { "keys": ["CTRL", "C"] } }
        },
        {
          "id": "paste",
          "label": "Paste",
          "icon": { "type": "material", "value": "content_paste" },
          "cell": { "row": 0, "column": 1 },
          "state": { "type": "momentary" },
          "on_press": { "type": "keyboard.shortcut", "params": { "keys": ["CTRL", "V"] } }
        },
        {
          "id": "undo",
          "label": "Undo",
          "icon": { "type": "material", "value": "undo" },
          "cell": { "row": 0, "column": 2 },
          "state": { "type": "momentary" },
          "on_press": { "type": "keyboard.shortcut", "params": { "keys": ["CTRL", "Z"] } }
        },
        {
          "id": "save",
          "label": "Save",
          "icon": { "type": "material", "value": "save" },
          "cell": { "row": 0, "column": 3 },
          "state": { "type": "momentary" },
          "on_press": { "type": "keyboard.shortcut", "params": { "keys": ["CTRL", "S"] } }
        },
        {
          "id": "terminal",
          "label": "Terminal",
          "icon": { "type": "emoji", "value": "🖥️" },
          "cell": { "row": 1, "column": 0 },
          "state": { "type": "momentary" },
          "permissions": ["apps"],
          "on_press": { "type": "open_terminal", "params": {} }
        },
        {
          "id": "editor",
          "label": "Editor",
          "icon": { "type": "emoji", "value": "📝" },
          "cell": { "row": 1, "column": 1 },
          "state": { "type": "momentary" },
          "permissions": ["apps"],
          "on_press": {
            "type": "launch_application",
            "params": {
              "target": {
                "windows": "notepad.exe",
                "linux": "gedit",
                "darwin": "TextEdit"
              }
            }
          }
        },
        {
          "id": "browser",
          "label": "Docs",
          "icon": { "type": "emoji", "value": "🌐" },
          "cell": { "row": 1, "column": 2 },
          "state": { "type": "momentary" },
          "permissions": ["apps"],
          "on_press": { "type": "open_url", "params": { "url": "https://developer.mozilla.org" } }
        },
        {
          "id": "media-page",
          "label": "Media",
          "icon": { "type": "emoji", "value": "🎵" },
          "cell": { "row": 1, "column": 3 },
          "state": { "type": "momentary" },
          "on_press": { "type": "deck.open_page", "params": { "page_id": "media" } }
        },
        {
          "id": "system-page",
          "label": "System",
          "icon": { "type": "emoji", "value": "📊" },
          "cell": { "row": 2, "column": 0 },
          "state": { "type": "momentary" },
          "on_press": { "type": "deck.open_page", "params": { "page_id": "system" } }
        },
        {
          "id": "switch-window",
          "label": "Alt+Tab",
          "icon": { "type": "material", "value": "swap_horiz" },
          "cell": { "row": 2, "column": 1 },
          "state": { "type": "momentary" },
          "on_press": { "type": "keyboard.shortcut", "params": { "keys": ["ALT", "TAB"] } }
        },
        {
          "id": "task-manager",
          "label": "Tasks",
          "icon": { "type": "material", "value": "monitor_heart" },
          "cell": { "row": 2, "column": 2 },
          "state": { "type": "momentary" },
          "on_press": {
            "type": "keyboard.shortcut",
            "params": {
              "keys": { "windows": ["CTRL", "SHIFT", "ESCAPE"], "linux": ["CTRL", "ALT", "T"] }
            }
          }
        },
        {
          "id": "lock",
          "label": "Lock",
          "icon": { "type": "material", "value": "lock" },
          "cell": { "row": 2, "column": 3 },
          "state": { "type": "momentary" },
          "permissions": ["system.power"],
          "on_press": { "type": "system.lock", "params": {} }
        },
        {
          "id": "cpu",
          "label": "CPU",
          "icon": { "type": "none" },
          "cell": { "row": 3, "column": 0 },
          "state": { "type": "telemetry", "metric": "cpu.usage", "format": "{value:.0f}%", "default": "--" },
          "on_press": { "type": "noop", "params": {} }
        },
        {
          "id": "ram",
          "label": "RAM",
          "icon": { "type": "none" },
          "cell": { "row": 3, "column": 1 },
          "state": { "type": "telemetry", "metric": "mem.used_pct", "format": "{value:.0f}%", "default": "--" },
          "on_press": { "type": "noop", "params": {} }
        },
        {
          "id": "disk",
          "label": "Disk",
          "icon": { "type": "none" },
          "cell": { "row": 3, "column": 2 },
          "state": { "type": "telemetry", "metric": "disk.used_pct", "format": "{value:.0f}%", "default": "--" },
          "on_press": { "type": "noop", "params": {} }
        },
        {
          "id": "notify",
          "label": "Hello",
          "icon": { "type": "emoji", "value": "👋" },
          "cell": { "row": 3, "column": 3 },
          "state": { "type": "momentary" },
          "on_press": { "type": "deck.notify", "params": { "message": "MobileDeck is connected", "level": "success" } }
        }
      ]
    },
    {
      "id": "media",
      "name": "Media",
      "parent": "home",
      "buttons": [
        {
          "id": "back",
          "label": "Back",
          "icon": { "type": "material", "value": "arrow_back" },
          "cell": { "row": 0, "column": 0 },
          "state": { "type": "momentary" },
          "on_press": { "type": "deck.back", "params": {} }
        },
        {
          "id": "prev",
          "label": "Previous",
          "icon": { "type": "material", "value": "skip_previous" },
          "cell": { "row": 0, "column": 1 },
          "state": { "type": "momentary" },
          "on_press": { "type": "media.previous", "params": {} }
        },
        {
          "id": "playpause",
          "label": "Play",
          "icon": { "type": "material", "value": "play_pause" },
          "cell": { "row": 0, "column": 2 },
          "state": { "type": "momentary" },
          "on_press": { "type": "media.play_pause", "params": {} }
        },
        {
          "id": "next",
          "label": "Next",
          "icon": { "type": "material", "value": "skip_next" },
          "cell": { "row": 0, "column": 3 },
          "state": { "type": "momentary" },
          "on_press": { "type": "media.next", "params": {} }
        },
        {
          "id": "vol-down",
          "label": "Vol -",
          "icon": { "type": "material", "value": "volume_down" },
          "cell": { "row": 1, "column": 0 },
          "state": { "type": "momentary" },
          "on_press": { "type": "volume.down", "params": {} }
        },
        {
          "id": "vol-up",
          "label": "Vol +",
          "icon": { "type": "material", "value": "volume_up" },
          "cell": { "row": 1, "column": 1 },
          "state": { "type": "momentary" },
          "on_press": { "type": "volume.up", "params": {} }
        },
        {
          "id": "mute",
          "label": "Mute",
          "icon": { "type": "material", "value": "volume_off" },
          "cell": { "row": 1, "column": 2 },
          "state": { "type": "momentary" },
          "on_press": { "type": "volume.mute", "params": {} }
        },
        {
          "id": "stop",
          "label": "Stop",
          "icon": { "type": "material", "value": "stop" },
          "cell": { "row": 1, "column": 3 },
          "state": { "type": "momentary" },
          "on_press": { "type": "media.stop", "params": {} }
        },
        {
          "id": "macro-demo",
          "label": "Macro",
          "sublabel": "type + enter",
          "icon": { "type": "emoji", "value": "⌨️" },
          "cell": { "row": 2, "column": 0, "column_span": 2 },
          "state": { "type": "momentary" },
          "on_press": {
            "type": "macro",
            "params": {
              "steps": [
                { "type": "delay", "params": { "ms": 100 } },
                { "type": "keyboard.text", "params": { "text": "mobiledeck", "interval_ms": 20 } },
                { "type": "keyboard.key", "params": { "key": "ENTER" } }
              ]
            }
          }
        },
        {
          "id": "counter-demo",
          "label": "Toggle",
          "icon": { "type": "emoji", "value": "🔀" },
          "cell": { "row": 2, "column": 2 },
          "state": { "type": "toggle" },
          "on_press": { "type": "noop", "params": {} }
        }
      ]
    },
    {
      "id": "system",
      "name": "System",
      "parent": "home",
      "buttons": [
        {
          "id": "back",
          "label": "Back",
          "icon": { "type": "material", "value": "arrow_back" },
          "cell": { "row": 0, "column": 0 },
          "state": { "type": "momentary" },
          "on_press": { "type": "deck.back", "params": {} }
        },
        {
          "id": "stats",
          "label": "Stats",
          "icon": { "type": "material", "value": "insights" },
          "cell": { "row": 0, "column": 1 },
          "state": { "type": "momentary" },
          "on_press": { "type": "system.stats", "params": {} }
        },
        {
          "id": "lock2",
          "label": "Lock",
          "icon": { "type": "material", "value": "lock" },
          "cell": { "row": 0, "column": 2 },
          "state": { "type": "momentary" },
          "permissions": ["system.power"],
          "on_press": { "type": "system.lock", "params": {} }
        },
        {
          "id": "shutdown",
          "label": "Shut down",
          "sublabel": "asks first",
          "icon": { "type": "material", "value": "power_settings_new" },
          "background": "#5a1f1f",
          "cell": { "row": 0, "column": 3 },
          "state": { "type": "momentary" },
          "permissions": ["system.power"],
          "on_press": {
            "type": "system.shutdown",
            "params": { "confirm": true },
            "require_confirmation": true
          }
        },
        {
          "id": "media-back",
          "label": "Media",
          "icon": { "type": "emoji", "value": "🎵" },
          "cell": { "row": 1, "column": 0 },
          "state": { "type": "momentary" },
          "on_press": { "type": "deck.open_page", "params": { "page_id": "media" } }
        }
      ]
    }
  ]
}
`
