package sounds

import "testing"

// TestDisplayName covers the label rule the panel shows next to a file. It is
// the only user-visible transformation of a name, so the edge cases (a file
// whose stem is entirely separators, a dotted stem) are pinned here.
func TestDisplayName(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		{"boom.wav", "boom"},
		{"my_sound.mp3", "my sound"},
		{"air-horn.ogg", "air horn"},
		{"my_cool-sound.flac", "my cool sound"},
		{"UPPER.WAV", "UPPER"},
		{"a.b.c.wav", "a.b.c"},
		{"---.wav", "---"},
		{"___.wav", "___"},
		{" spaced .wav", "spaced"},
	}
	for _, tc := range cases {
		if got := DisplayName(tc.file); got != tc.want {
			t.Errorf("DisplayName(%q) = %q, want %q", tc.file, got, tc.want)
		}
	}
}

// TestCleanName covers the validation that turns an untrusted name into a path
// component. Anything that is not a bare file name with an accepted extension
// must be refused with an error, because the caller joins it to a directory.
func TestCleanName(t *testing.T) {
	valid := []string{"boom.wav", "a.mp3", "x.ogg", "y.flac", "z.m4a", "q.aac", "p.opus", "BOOM.WAV"}
	for _, name := range valid {
		if got, err := CleanName(name); err != nil || got != name {
			t.Errorf("CleanName(%q) = %q, %v; want it accepted unchanged", name, got, err)
		}
	}

	invalid := []string{
		"", "   ", " boom.wav", "boom.wav ",
		".", "..", "../boom.wav", "sub/boom.wav", `sub\boom.wav`,
		"/etc/passwd.wav", `C:\boom.wav`,
		".hidden.wav",
		"boom", "boom.exe", "boom.wav.txt", "boom.",
		"boom\x00.wav",
	}
	for _, name := range invalid {
		if got, err := CleanName(name); err == nil {
			t.Errorf("CleanName(%q) = %q, want an error", name, got)
		}
	}

	// The length cap is a real limit, not an accident of the extension check.
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := CleanName(string(long) + ".wav"); err == nil {
		t.Error("a name over 200 bytes was accepted")
	}
}
