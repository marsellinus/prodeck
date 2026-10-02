// Package sounds owns what a sound file is: which extensions are accepted,
// what MIME type each carries, how a name is sanitised, and how an upload's
// format is recognised from its first bytes.
//
// It exists because two callers need those facts and must not disagree: the
// admin API (which lists, stores and previews a file) and the sound.play action
// (which refuses a file this host cannot play). A second copy of the extension
// table would eventually accept an upload the action refuses, which is exactly
// the "button that looks configured and does nothing" failure the profile
// validation exists to prevent.
package sounds

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// MaxFileBytes caps one sound file. It is 8 MiB because that is also the cap on
// the desktop panel's bridge read (internal/gui): a larger file would be
// silently truncated while previewing, so it is refused at upload instead.
const MaxFileBytes = 8 << 20

// MaxUploadBodyBytes caps the JSON body of an upload. Base64 inflates by 4/3,
// so the body limit sits above MaxFileBytes with room for the envelope.
const MaxUploadBodyBytes = 16 << 20

// mimeByExt maps every extension a profile may reference to the MIME type the
// panel needs for a data URL.
//
// .opus is served as audio/ogg because Opus is carried in an Ogg container and
// that is what a browser decodes; the extension is still the one a profile
// writes.
var mimeByExt = map[string]string{
	".wav":  "audio/wav",
	".mp3":  "audio/mpeg",
	".ogg":  "audio/ogg",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".opus": "audio/ogg",
}

// Extensions returns the accepted extensions, sorted, for error messages and
// docs.
func Extensions() []string {
	out := make([]string, 0, len(mimeByExt))
	for ext := range mimeByExt {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

// ExtensionList renders the accepted extensions for a human.
func ExtensionList() string { return strings.Join(Extensions(), " ") }

// MIME returns the content type for a file name, and whether the extension is
// one this host accepts.
func MIME(name string) (string, bool) {
	mime, ok := mimeByExt[strings.ToLower(filepath.Ext(name))]
	return mime, ok
}

// IsAudio reports whether a file name carries an accepted audio extension.
// This is the filter for a directory listing: any other file is not a sound.
func IsAudio(name string) bool {
	_, ok := MIME(name)
	return ok
}

// CleanName validates a file name and returns it unchanged when it is safe.
//
// A sound name arrives from a profile or from an upload and becomes a path, so
// it must be a bare file name: no separators, no parent reference, no NUL, and
// an extension this host accepts. The confinement check in the action is the
// second barrier; this one produces the better message.
func CleanName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("the file name must not be empty")
	}
	if trimmed != name {
		return "", fmt.Errorf("the file name %q must not start or end with whitespace", name)
	}
	if trimmed == "." || trimmed == ".." {
		return "", fmt.Errorf("%q is not a file name", name)
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return "", fmt.Errorf("the file name %q must be a bare file name, without a directory", name)
	}
	if strings.ContainsRune(trimmed, 0) {
		return "", fmt.Errorf("the file name must not contain a NUL byte")
	}
	if strings.HasPrefix(trimmed, ".") {
		return "", fmt.Errorf("the file name %q must not start with a dot", name)
	}
	if len(trimmed) > 200 {
		return "", fmt.Errorf("the file name is %d bytes; the maximum is 200", len(trimmed))
	}
	if _, ok := MIME(trimmed); !ok {
		return "", fmt.Errorf("%q has no audio extension; accepted extensions are %s", name, ExtensionList())
	}
	return trimmed, nil
}

// DisplayName is the label the panel shows for a file: the base name without
// its extension, with underscores and hyphens turned into spaces.
func DisplayName(file string) string {
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	label := strings.NewReplacer("_", " ", "-", " ").Replace(base)
	if collapsed := strings.Join(strings.Fields(label), " "); collapsed != "" {
		return collapsed
	}
	// A file called "---.wav" would otherwise show as an empty pad.
	return base
}

// Sniff recognises an audio format from the first bytes of a file, for an
// upload whose name carries no extension. It is deliberately a small table of
// the container signatures this host accepts rather than http.DetectContentType,
// which maps Ogg to application/ogg and does not know FLAC.
func Sniff(data []byte) (ext, mime string, ok bool) {
	if len(data) < 12 {
		return "", "", false
	}
	switch {
	case string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return ".wav", "audio/wav", true
	case string(data[0:3]) == "ID3":
		return ".mp3", "audio/mpeg", true
	case data[0] == 0xFF && data[1]&0xF6 == 0xF0:
		// ADTS AAC: a 12-bit 0xFFF sync with layer bits 00. An MPEG audio
		// frame sync is 11 bits and leaves bit 1 set, which the mask excludes.
		return ".aac", "audio/aac", true
	case data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		return ".mp3", "audio/mpeg", true
	case string(data[0:4]) == "OggS":
		return ".ogg", "audio/ogg", true
	case string(data[0:4]) == "fLaC":
		return ".flac", "audio/flac", true
	case string(data[4:8]) == "ftyp":
		return ".m4a", "audio/mp4", true
	}
	return "", "", false
}
