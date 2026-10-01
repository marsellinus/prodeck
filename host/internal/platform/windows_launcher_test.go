//go:build windows

package platform

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestQuoteWindowsArg(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Nothing special: passed through untouched, which is what the C
		// runtime reads back verbatim.
		{"plain", "plain"},
		{`back\slash`, `back\slash`},
		{"", `""`},
		{"one two", `"one two"`},
		{`three"four`, `"three\"four"`},
		{"a & b", `"a & b"`},
		{"tab\there", "\"tab\there\""},
		// A trailing backslash would otherwise escape the closing quote, so it
		// is doubled.
		{`two words\`, `"two words\\"`},
		// Backslashes before a quote are doubled and the quote escaped.
		{`a"b\`, `"a\"b\\"`},
		{`a\"b`, `"a\\\"b"`},
	}
	for _, tc := range tests {
		if got := quoteWindowsArg(tc.in); got != tc.want {
			t.Errorf("quoteWindowsArg(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestJoinArgsRejectsCmdMetacharacters(t *testing.T) {
	// A batch file's arguments are re-parsed by cmd.exe, whose grammar is not
	// the C runtime's; an argument cmd would interpret is refused rather than
	// quoted and hoped for.
	if _, err := joinArgs("script.cmd", []string{"a & b"}); err == nil {
		t.Errorf("joinArgs(script.cmd, [a & b]) should have been refused")
	}
	for _, meta := range []string{"a|b", "a>b", "a<b", "a^b"} {
		if _, err := joinArgs("script.cmd", []string{meta}); err == nil {
			t.Errorf("joinArgs(script.cmd, [%s]) should have been refused", meta)
		}
	}
	// The same argument is fine for a document, which is not parsed by cmd.
	got, err := joinArgs("doc.pdf", []string{"a & b"})
	if err != nil {
		t.Fatalf("joinArgs(doc.pdf): %v", err)
	}
	if got != `"a & b"` {
		t.Errorf("joinArgs(doc.pdf) = %s, want \"a & b\"", got)
	}
	// Harmless arguments are quoted normally for a batch file.
	got, err = joinArgs("script.cmd", []string{"plain", "two words"})
	if err != nil {
		t.Fatalf("joinArgs(script.cmd, plain args): %v", err)
	}
	if got != `plain "two words"` {
		t.Errorf("joinArgs(script.cmd) = %s, want plain \"two words\"", got)
	}
}

func TestWindowsExecutable(t *testing.T) {
	// A bare name is resolved against PATH and PATHEXT.
	found, ok := windowsExecutable("cmd")
	if !ok {
		t.Fatalf("windowsExecutable(\"cmd\") not recognised")
	}
	if !strings.EqualFold(filepath.Ext(found), ".exe") {
		t.Errorf("windowsExecutable(\"cmd\") = %q, want an .exe", found)
	}

	// A batch file is not an image: CreateProcess cannot run it, so it must go
	// to the shell.
	if _, ok := windowsExecutable("C:\\Windows\\System32\\drivers\\etc\\hosts"); ok {
		t.Errorf("a non-executable file was reported as a Windows image")
	}
	if _, ok := windowsExecutable("definitely-not-on-path-xyz"); ok {
		t.Errorf("a missing name was reported as a Windows image")
	}
}

func TestIsWindowsImage(t *testing.T) {
	for _, want := range []string{"a.exe", "A.EXE", "a.com"} {
		if !isWindowsImage(want) {
			t.Errorf("isWindowsImage(%q) = false, want true", want)
		}
	}
	for _, not := range []string{"a.cmd", "a.bat", "a.ps1", "a", "a.txt"} {
		if isWindowsImage(not) {
			t.Errorf("isWindowsImage(%q) = true, want false", not)
		}
	}
}
