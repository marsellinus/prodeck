package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mobiledeck/mobiledeck/host/internal/sounds"
)

// The sound endpoints back the soundboard in the desktop panel.
//
// They are a small, closed surface: every file lives in one directory, its name
// is a bare file name with an audio extension, and a file a profile still uses
// cannot be deleted. That confinement is what lets a sound be referenced by name
// from a profile with no path check at press time beyond the action's own.
//
// The audio endpoint returns base64 JSON rather than raw bytes because the
// panel reaches the host through a bridge that returns the body as a string and
// deliberately keeps the admin token out of JavaScript (see the panel's
// previewSound). A plain audio response would be unreadable to it.

// soundView is the shape the panel reads for one sound.
type soundView struct {
	File  string `json:"file"`
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	MIME  string `json:"mime"`
}

// adminSounds lists the sounds directory and stores an upload.
func (s *Server) adminSounds(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		dir, err := s.soundsDir()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		out := make([]soundView, 0, len(entries))
		for _, e := range entries {
			// Only files with an accepted extension are sounds. Anything else in
			// the directory (a stray README, an editor backup) is not offered,
			// because the panel would show it and pressing it would fail.
			if e.IsDir() || !sounds.IsAudio(e.Name()) {
				continue
			}
			mime, _ := sounds.MIME(e.Name())
			info, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, soundView{
				File:  e.Name(),
				Name:  sounds.DisplayName(e.Name()),
				Bytes: info.Size(),
				MIME:  mime,
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
		writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "sounds": out})

	case http.MethodPost:
		s.adminSoundUpload(w, r)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

// adminSoundUpload stores one uploaded sound.
//
// The body is a JSON object with a base64 payload rather than a multipart
// upload: the panel already has the bytes in the browser, and one JSON shape
// keeps the panel's bridge to a single function.
func (s *Server) adminSoundUpload(w http.ResponseWriter, r *http.Request) {
	dir, err := s.soundsDir()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}

	var body struct {
		Name string `json:"name"`
		Data string `json:"data"`
	}
	// The body limit is sounds.MaxUploadBodyBytes, above the file cap by the
	// base64 inflation factor, so a legal file is never refused as an oversized
	// body and an oversized body is refused before it is decoded.
	if err := decodeBody(w, r, &body, sounds.MaxUploadBodyBytes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}

	name, err := sounds.CleanName(body.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.Data))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "data is not valid base64: "+err.Error())
		return
	}
	if len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_argument", "the file is empty")
		return
	}
	if len(raw) > sounds.MaxFileBytes {
		writeError(w, http.StatusBadRequest, "invalid_argument",
			fmt.Sprintf("the file is %d bytes; the maximum is %d", len(raw), sounds.MaxFileBytes))
		return
	}

	path := filepath.Join(dir, name)
	// O_EXCL makes the "already taken" answer atomic: two uploads of the same
	// name cannot both succeed, which a check-then-write would allow.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			writeError(w, http.StatusConflict, "conflict", name+" is already on this host; remove it first or rename the file")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	mime, _ := sounds.MIME(name)
	s.log.Info("sound stored", "file", name, "bytes", len(raw))
	writeJSON(w, http.StatusCreated, soundView{
		File:  name,
		Name:  sounds.DisplayName(name),
		Bytes: int64(len(raw)),
		MIME:  mime,
	})
}

// adminSound handles one sound: DELETE removes it.
func (s *Server) adminSound(w http.ResponseWriter, r *http.Request, file string) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use DELETE")
		return
	}
	dir, err := s.soundsDir()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	name, err := sounds.CleanName(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no sound "+name)
		return
	}
	// A directory whose name happens to end in .wav is not a sound; answering
	// "not found" is truer than a 500 from trying to unlink a non-empty tree.
	if info.IsDir() {
		writeError(w, http.StatusNotFound, "not_found", "no sound "+name)
		return
	}
	// A file a profile still plays must not disappear: the button would keep
	// looking configured and silently do nothing the next time it is pressed.
	if s.engine.SoundReferenced(name) {
		writeError(w, http.StatusConflict, "conflict",
			name+" is still used by a profile; remove the button that plays it first")
		return
	}
	if err := os.Remove(path); err != nil {
		// The file can vanish between the check above and the unlink (another
		// delete, a cleanup script); that is the same outcome the caller asked
		// for, not an internal error.
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not_found", "no sound "+name)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.log.Info("sound removed", "file", name)
	w.WriteHeader(http.StatusNoContent)
}

// adminSoundAudio returns one sound's bytes as base64 JSON, for the panel's
// preview player.
func (s *Server) adminSoundAudio(w http.ResponseWriter, r *http.Request, file string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	dir, err := s.soundsDir()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	name, err := sounds.CleanName(file)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no sound "+file)
		return
	}
	// The path is joined from a validated bare name, so it cannot escape the
	// directory; the check below makes that guarantee explicit at the point the
	// file is read rather than relying on the reader remembering it.
	path := filepath.Join(dir, name)
	if !pathWithin(dir, path) {
		writeError(w, http.StatusNotFound, "not_found", "no sound "+file)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no sound "+name)
		return
	}
	mime, _ := sounds.MIME(name)
	writeJSON(w, http.StatusOK, map[string]any{
		"file": name,
		"mime": mime,
		"data": base64.StdEncoding.EncodeToString(raw),
	})
}

// soundsDir returns the absolute sounds directory, creating it if needed.
//
// It is created on demand rather than at start-up so a host that never uses the
// soundboard leaves no empty directory behind, and so the very first upload
// works on a fresh configuration.
func (s *Server) soundsDir() (string, error) {
	dir := strings.TrimSpace(s.cfg.SoundsDir)
	if dir == "" {
		return "", fmt.Errorf("no sounds directory is configured on this host")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolving the sounds directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("creating the sounds directory: %w", err)
	}
	return abs, nil
}

// pathWithin reports whether child is root or lives inside it.
func pathWithin(root, child string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
