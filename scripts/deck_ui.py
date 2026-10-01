#!/usr/bin/env python3
"""Drive the MobileDeck Android app on a real device over adb.

Developer tooling for the manual end-to-end check, not part of the product.

Why this exists: `adb shell input text` throws an internal NullPointerException
on some LineageOS builds, and synthetic taps do not always move focus to a
Compose text field. So every field interaction here is coordinate-based, read
fresh from the UI hierarchy, and verified afterwards.

Usage:
  python scripts/deck_ui.py dump
  python scripts/deck_ui.py host <address>
  python scripts/deck_ui.py connect
  python scripts/deck_ui.py pair <pin>
  python scripts/deck_ui.py wait-deck
  python scripts/deck_ui.py tap <text>
"""

import re
import subprocess
import sys
import time
import xml.etree.ElementTree as ET

# Keycodes for `adb shell input keyevent`.
DIGIT = {str(d): str(7 + d) for d in range(10)}
KEY = {
    ".": "56",
    "-": "69",
    "BACKSPACE": "67",
    "ENTER": "66",
    "MOVE_END": "123",
}
# Characters that need a modifier, produced the way a keyboard does it.
COMBOS = {":": ("59", "74"), "_": ("59", "69")}


def adb(*args, timeout=60):
    return subprocess.run(
        ["adb", *args], capture_output=True, text=True, timeout=timeout
    ).stdout


def nodes():
    adb("shell", "uiautomator", "dump", "/sdcard/deck_ui.xml")
    raw = adb("shell", "cat", "/sdcard/deck_ui.xml")
    i = raw.find("<?xml")
    if i < 0:
        return []
    out = []
    for n in ET.fromstring(raw[i:]).iter():
        out.append({
            "class": n.get("class", "").split(".")[-1],
            "text": n.get("text", "") or n.get("content-desc", ""),
            "bounds": n.get("bounds", ""),
            "focused": n.get("focused") == "true",
        })
    return out


def center(bounds):
    v = [int(x) for x in re.findall(r"-?\d+", bounds)]
    return (v[0] + v[2]) // 2, (v[1] + v[3]) // 2


def tap(node):
    x, y = center(node["bounds"])
    adb("shell", "input", "tap", str(x), str(y))


def edit_fields():
    return [n for n in nodes() if n["class"] == "EditText"]


def field(index):
    """
    The EditText at `index`, counted from the top of the screen.

    Counting from the top rather than searching by label is what makes this
    reliable: the pairing card and the manual-host card scroll independently, so
    a field's absolute y changes, but their vertical order does not.
    """
    fields = sorted(edit_fields(), key=lambda n: center(n["bounds"])[1])
    return fields[index] if index < len(fields) else None


def focused_field():
    return next((n for n in edit_fields() if n["focused"]), None)


def type_text(s):
    for ch in s:
        if ch in COMBOS:
            shift, key = COMBOS[ch]
            adb("shell", "input", "keycombination", shift, key)
        elif ch in DIGIT:
            adb("shell", "input", "keyevent", DIGIT[ch])
        elif ch in KEY:
            adb("shell", "input", "keyevent", KEY[ch])
        else:
            raise SystemExit(f"no key code for {ch!r}")
        time.sleep(0.12)


def focus_and_type(index, text, clear=0):
    """
    Tap the field at `index`, confirm it took focus, then type into it.

    The bounds are re-read on every attempt because the cards scroll as banners
    appear and disappear, and a coordinate from a moment ago can be stale. The
    focus check is the guard against the failure that cost the most time: typing
    a PIN into the manual-host field because the tap landed on the wrong card.
    """
    for _ in range(6):
        target = field(index)
        if target is None:
            print(f"no EditText at index {index}")
            return False
        tap(target)
        time.sleep(0.9)

        focused = focused_field()
        if focused is not None and center(focused["bounds"])[1] == center(target["bounds"])[1]:
            if clear:
                adb("shell", "input", "keyevent", KEY["MOVE_END"])
                for _ in range(clear):
                    adb("shell", "input", "keyevent", KEY["BACKSPACE"])
                time.sleep(0.4)
            type_text(text)
            time.sleep(0.7)
            return True
        time.sleep(0.5)
    return False


def step_dump():
    for n in nodes():
        if n["text"]:
            print(f'{n["class"]:12} {n["text"]!r:44} {n["bounds"]}')
    print("--- fields ---")
    for n in edit_fields():
        print(f'  EditText {n["text"]!r:34} focused={n["focused"]} {n["bounds"]}')


def step_host(addr):
    # On the connect screen the host field is the first EditText.
    if not focus_and_type(0, addr, clear=40):
        print("could not focus the host field")
        return False
    cur = field(0)
    print("host field:", repr(cur["text"]) if cur else "<none>")
    return bool(cur and cur["text"] == addr)


def step_connect():
    btn = next((n for n in nodes() if n["text"] == "Connect"), None)
    if btn is None:
        print("no Connect button")
        return False
    tap(btn)
    for _ in range(40):
        time.sleep(0.5)
        if any(n["text"] == "Pair" for n in nodes()):
            print("pairing card is up")
            return True
    print("did not reach the pairing screen")
    return False


def step_pair(pin):
    # On the pairing screen the PIN field is the first EditText and the manual
    # host field is the second.
    if not focus_and_type(0, pin, clear=12):
        print("could not focus the PIN field")
        return False

    cur = field(0)
    got = cur["text"] if cur else ""
    print("PIN field:", repr(got))
    if got != pin:
        print(f"the PIN did not land in the field (got {got!r}); aborting")
        for n in edit_fields():
            print("   field:", repr(n["text"]), n["bounds"])
        return False

    btn = next((n for n in nodes() if n["text"] == "Pair"), None)
    if btn is None:
        print("no Pair button")
        return False
    tap(btn)
    print("tapped Pair")
    return True


def step_wait_deck():
    for _ in range(60):
        time.sleep(0.5)
        ns = nodes()
        if any(n["text"] in ("Copy", "Paste", "Terminal", "CPU", "RAM") for n in ns):
            print("DECK VISIBLE")
            return True
        err = [n["text"] for n in ns if "not accepting" in n["text"] or "failed" in n["text"].lower()]
        if err:
            print("host reported:", err)
            return False
    print("the deck never appeared")
    return False


def step_tap(text):
    n = next((n for n in nodes() if n["text"] == text), None)
    if n is None:
        print(f"no node with text {text!r}")
        return False
    tap(n)
    print(f"tapped {text!r}")
    return True


def main():
    if len(sys.argv) < 2:
        raise SystemExit(__doc__)
    step = sys.argv[1]
    ok = {
        "dump": lambda: (step_dump(), True)[1],
        "host": lambda: step_host(sys.argv[2]),
        "connect": step_connect,
        "pair": lambda: step_pair(sys.argv[2]),
        "wait-deck": step_wait_deck,
        "tap": lambda: step_tap(sys.argv[2]),
    }[step]()
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
