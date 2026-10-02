package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mobiledeck/mobiledeck/host/internal/profile"
	"github.com/mobiledeck/mobiledeck/host/internal/sounds"
)

// The sound endpoints back the soundboard in the desktop panel. The tests below
// cover the guarantees the panel depends on: a file is confined to the sounds
// directory, a delete that would break a button is refused, and the audio
// endpoint returns base64 JSON rather than raw bytes.

// wavBytes is a minimal, valid RIFF/WAVE header. The tests never decode it, so
// the audio content does not matter; only that the bytes survive the round trip
// byte for byte.
func wavBytes() []byte {
	raw := make([]byte, 44)
	copy(raw[0:4], "RIFF")
	copy(raw[8:12], "WAVE")
	copy(raw[12:16], "fmt ")
	copy(raw[36:40], "data")
	return raw
}

// uploadSound stores a sound through the admin API and returns the response.
func uploadSound(t *testing.T, ts *testServer, name string, data []byte) (int, []byte) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"name": name,
		"data": base64.StdEncoding.EncodeToString(data),
	})
	return adminDo(t, ts, http.MethodPost, "/api/v1/admin/sounds", string(body))
}

// TestAdminSoundsUploadListDelete covers the happy path end to end, including
// that the audio endpoint is JSON with base64 rather than a raw audio stream.
func TestAdminSoundsUploadListDelete(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	raw := wavBytes()

	code, body := uploadSound(t, ts, "boom.wav", raw)
	if code != http.StatusCreated {
		t.Fatalf("POST /sounds: %d %s", code, body)
	}
	var created struct {
		File  string `json:"file"`
		Name  string `json:"name"`
		Bytes int64  `json:"bytes"`
		MIME  string `json:"mime"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decoding the upload response: %v", err)
	}
	if created.File != "boom.wav" || created.Name != "boom" || created.Bytes != int64(len(raw)) || created.MIME != "audio/wav" {
		t.Fatalf("upload response = %+v", created)
	}

	// The file is on disk with the exact bytes.
	onDisk, err := os.ReadFile(filepath.Join(ts.dir, "sounds", "boom.wav"))
	if err != nil {
		t.Fatalf("reading the stored file: %v", err)
	}
	if string(onDisk) != string(raw) {
		t.Error("the stored bytes differ from the uploaded bytes")
	}

	// The list shows it with a display name.
	code, body = adminDo(t, ts, http.MethodGet, "/api/v1/admin/sounds", "")
	if code != http.StatusOK {
		t.Fatalf("GET /sounds: %d %s", code, body)
	}
	var list struct {
		Dir    string `json:"dir"`
		Sounds []struct {
			File  string `json:"file"`
			Name  string `json:"name"`
			Bytes int64  `json:"bytes"`
			MIME  string `json:"mime"`
		} `json:"sounds"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decoding the list: %v", err)
	}
	if list.Dir == "" {
		t.Error("the list has no dir")
	}
	if len(list.Sounds) != 1 || list.Sounds[0].File != "boom.wav" || list.Sounds[0].Name != "boom" {
		t.Fatalf("list = %+v", list.Sounds)
	}

	// The audio endpoint is JSON carrying base64, which is what the panel's
	// bridge can read. Raw bytes would be unreadable to it.
	code, body = adminDo(t, ts, http.MethodGet, "/api/v1/admin/sounds/boom.wav/audio", "")
	if code != http.StatusOK {
		t.Fatalf("GET /sounds/boom.wav/audio: %d %s", code, body)
	}
	if !json.Valid(body) {
		t.Fatalf("the audio response is not JSON: %.60s", body)
	}
	var audio struct {
		MIME string `json:"mime"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &audio); err != nil {
		t.Fatalf("decoding the audio response: %v", err)
	}
	if audio.MIME != "audio/wav" {
		t.Errorf("mime = %q", audio.MIME)
	}
	decoded, err := base64.StdEncoding.DecodeString(audio.Data)
	if err != nil {
		t.Fatalf("the audio data is not base64: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Error("the audio bytes differ from the uploaded bytes")
	}

	// A sound nothing references can be removed.
	code, body = adminDo(t, ts, http.MethodDelete, "/api/v1/admin/sounds/boom.wav", "")
	if code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(ts.dir, "sounds", "boom.wav")); !os.IsNotExist(err) {
		t.Error("the file is still on disk after a successful delete")
	}
	// Removing it again is a clear 404, not a 500.
	if code, _ := adminDo(t, ts, http.MethodDelete, "/api/v1/admin/sounds/boom.wav", ""); code != http.StatusNotFound {
		t.Errorf("a second DELETE returned %d, want 404", code)
	}
}

// TestAdminSoundsRejectsBadUploads covers the extension gate, the duplicate
// rule, and the base64 failure, all of which the panel surfaces verbatim.
func TestAdminSoundsRejectsBadUploads(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	raw := wavBytes()

	if code, body := uploadSound(t, ts, "boom.exe", raw); code != http.StatusBadRequest {
		t.Errorf("an unsupported extension returned %d: %s", code, body)
	}
	if code, body := uploadSound(t, ts, "boom", raw); code != http.StatusBadRequest {
		t.Errorf("a name with no extension returned %d: %s", code, body)
	}
	if code, body := uploadSound(t, ts, "../escape.wav", raw); code != http.StatusBadRequest {
		t.Errorf("a traversal name returned %d: %s", code, body)
	}
	if code, body := uploadSound(t, ts, "boom.wav", nil); code != http.StatusBadRequest {
		t.Errorf("an empty file returned %d: %s", code, body)
	}
	// Names a filesystem cannot store are a 400, not a 500 from the write.
	for _, bad := range []string{"con.wav", "NUL.wav", `a<b.wav`, "a:b.wav", "a?b.wav"} {
		if code, body := uploadSound(t, ts, bad, raw); code != http.StatusBadRequest {
			t.Errorf("the name %q returned %d, want 400: %s", bad, code, body)
		}
	}

	// A body whose data is not base64 is refused before anything is written.
	bad := fmt.Sprintf(`{"name":"boom.wav","data":"not base64!!"}`)
	if code, body := adminDo(t, ts, http.MethodPost, "/api/v1/admin/sounds", bad); code != http.StatusBadRequest {
		t.Errorf("invalid base64 returned %d: %s", code, body)
	}

	// The first upload succeeds; the second of the same name is a conflict.
	if code, body := uploadSound(t, ts, "boom.wav", raw); code != http.StatusCreated {
		t.Fatalf("first upload: %d %s", code, body)
	}
	if code, body := uploadSound(t, ts, "boom.wav", raw); code != http.StatusConflict {
		t.Errorf("a duplicate upload returned %d, want 409: %s", code, body)
	}
}

// TestAdminSoundsRefusesAnOversizedFile covers the per-file cap, which is what
// keeps a sound previewable: a file above it would encode past the panel
// bridge's read limit. The body cap test above covers a different thing.
func TestAdminSoundsRefusesAnOversizedFile(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	oversized := make([]byte, sounds.MaxFileBytes+1)
	copy(oversized, "RIFF")
	copy(oversized[8:], "WAVE")
	if code, body := uploadSound(t, ts, "huge.wav", oversized); code != http.StatusBadRequest {
		t.Fatalf("a file over the cap returned %d, want 400: %s", code, body)
	}
	if entries, err := os.ReadDir(filepath.Join(ts.dir, "sounds")); err == nil && len(entries) != 0 {
		t.Errorf("an oversized upload left %d files behind", len(entries))
	}
}

// TestAdminSoundsRefusesAnOversizedBody covers the body cap. It is separate
// from the per-file cap because it protects a different thing: the file cap
// bounds what is stored, the body cap bounds what is buffered while decoding.
func TestAdminSoundsRefusesAnOversizedBody(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	// A body just over the limit. The data field is never decoded, so its
	// contents do not matter; only its length does.
	oversized := fmt.Sprintf(`{"name":"big.wav","data":"%s"}`,
		strings.Repeat("A", sounds.MaxUploadBodyBytes+1024))
	code, body := adminDo(t, ts, http.MethodPost, "/api/v1/admin/sounds", oversized)
	if code != http.StatusBadRequest {
		t.Fatalf("an oversized body returned %d, want 400: %s", code, body)
	}
	// Nothing was written.
	if entries, err := os.ReadDir(filepath.Join(ts.dir, "sounds")); err == nil && len(entries) != 0 {
		t.Errorf("an oversized upload left %d files behind", len(entries))
	}
}

// TestAdminSoundsRefusesToDeleteAReferencedFile is the reason the delete check
// exists: a button that keeps looking configured must not be silently broken by
// removing the file underneath it.
func TestAdminSoundsRefusesToDeleteAReferencedFile(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	raw := wavBytes()
	if code, body := uploadSound(t, ts, "boom.wav", raw); code != http.StatusCreated {
		t.Fatalf("upload: %d %s", code, body)
	}

	// Point a button at it, through the whole-document path the editor uses.
	code, body := adminDo(t, ts, http.MethodGet, "/api/v1/admin/profiles/development/document", "")
	if code != http.StatusOK {
		t.Fatalf("GET document: %d %s", code, body)
	}
	var doc profile.Profile
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	doc.Pages[0].Buttons[0].OnPress = &profile.Action{
		Type:   "sound.play",
		Params: json.RawMessage(`{"file":"boom.wav"}`),
	}
	updated, _ := json.Marshal(doc)
	if code, body := adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/development/document", string(updated)); code != http.StatusOK {
		t.Fatalf("PUT document: %d %s", code, body)
	}

	code, body = adminDo(t, ts, http.MethodDelete, "/api/v1/admin/sounds/boom.wav", "")
	if code != http.StatusConflict {
		t.Fatalf("DELETE of a referenced sound returned %d, want 409: %s", code, body)
	}
	if !strings.Contains(string(body), "still used") {
		t.Errorf("the conflict message does not explain itself: %s", body)
	}
	if _, err := os.Stat(filepath.Join(ts.dir, "sounds", "boom.wav")); err != nil {
		t.Error("the file was removed despite being referenced")
	}

	// Once the reference is gone, the delete succeeds.
	doc.Pages[0].Buttons[0].OnPress = &profile.Action{Type: "noop", Params: json.RawMessage(`{}`)}
	updated, _ = json.Marshal(doc)
	if code, body := adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/development/document", string(updated)); code != http.StatusOK {
		t.Fatalf("PUT document: %d %s", code, body)
	}
	if code, body := adminDo(t, ts, http.MethodDelete, "/api/v1/admin/sounds/boom.wav", ""); code != http.StatusNoContent {
		t.Fatalf("DELETE after removing the reference returned %d: %s", code, body)
	}
}

// TestAdminSoundsConfinesPaths checks the confinement on the read paths: a name
// that is not a bare file name must never resolve outside the sounds directory,
// however it is encoded.
func TestAdminSoundsConfinesPaths(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	// A file outside the sounds directory that a traversal would reach.
	secret := filepath.Join(ts.dir, "secret.wav")
	if err := os.WriteFile(secret, wavBytes(), 0o600); err != nil {
		t.Fatalf("writing the secret: %v", err)
	}

	for _, target := range []string{
		"../secret.wav",
		"..%2Fsecret.wav",
		"..\\secret.wav",
		"sub/secret.wav",
	} {
		path := "/api/v1/admin/sounds/" + target + "/audio"
		if code, _ := adminDo(t, ts, http.MethodGet, path, ""); code == http.StatusOK {
			t.Errorf("GET %s returned the file", path)
		}
		del := "/api/v1/admin/sounds/" + target
		if code, _ := adminDo(t, ts, http.MethodDelete, del, ""); code == http.StatusNoContent {
			t.Errorf("DELETE %s removed a file", del)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Error("a traversal delete removed a file outside the sounds directory")
	}
}

// TestAdminSoundsListShowsOnlyPlayableFiles checks that an entry the preview
// could not serve (a symlink out of the directory, a broken link, a directory
// named like a sound) is not offered, so the panel never shows a row that fails
// when pressed.
func TestAdminSoundsListShowsOnlyPlayableFiles(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	dir := filepath.Join(ts.dir, "sounds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.wav"), wavBytes(), 0o600); err != nil {
		t.Fatalf("writing the good sound: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "trap.wav"), 0o700); err != nil {
		t.Fatalf("mkdir trap: %v", err)
	}
	// A broken link and a link pointing outside; both are unplayable.
	_ = os.Symlink(filepath.Join(dir, "missing.wav"), filepath.Join(dir, "broken.wav"))
	secret := filepath.Join(ts.dir, "secret.wav")
	if err := os.WriteFile(secret, wavBytes(), 0o600); err != nil {
		t.Fatalf("writing the secret: %v", err)
	}
	_ = os.Symlink(secret, filepath.Join(dir, "escape.wav"))

	code, body := adminDo(t, ts, http.MethodGet, "/api/v1/admin/sounds", "")
	if code != http.StatusOK {
		t.Fatalf("GET /sounds: %d %s", code, body)
	}
	var list struct {
		Sounds []struct {
			File string `json:"file"`
		} `json:"sounds"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(list.Sounds) != 1 || list.Sounds[0].File != "good.wav" {
		t.Fatalf("list = %+v, want only good.wav", list.Sounds)
	}
}

// TestAdminSoundsAudioRefusesASymlinkEscape covers the subtle read escape: a
// link planted in the sounds directory that points at a file elsewhere must not
// be served, because the name check alone would accept it.
func TestAdminSoundsAudioRefusesASymlinkEscape(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	secret := filepath.Join(ts.dir, "secret.wav")
	if err := os.WriteFile(secret, wavBytes(), 0o600); err != nil {
		t.Fatalf("writing the secret: %v", err)
	}
	dir := filepath.Join(ts.dir, "sounds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "escape.wav")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	code, body := adminDo(t, ts, http.MethodGet, "/api/v1/admin/sounds/escape.wav/audio", "")
	if code == http.StatusOK {
		t.Fatalf("the audio endpoint served a file outside the sounds directory: %s", body)
	}
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

// TestAdminSoundsDeleteRejectsADirectory checks that a directory whose name
// happens to end in an audio extension is reported as "not found" rather than
// producing a 500 from trying to unlink it.
func TestAdminSoundsDeleteRejectsADirectory(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	dir := filepath.Join(ts.dir, "sounds")
	if err := os.MkdirAll(filepath.Join(dir, "trap.wav"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if code, body := adminDo(t, ts, http.MethodDelete, "/api/v1/admin/sounds/trap.wav", ""); code != http.StatusNotFound {
		t.Fatalf("DELETE of a directory returned %d, want 404: %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(dir, "trap.wav")); err != nil {
		t.Error("the directory was removed")
	}
}

// TestAdminSoundsDeleteIsCaseInsensitiveOnWindows covers a real platform trap:
// the filesystem is case-insensitive there, so a profile naming "Boom.wav" and
// a delete of "boom.wav" are the same file, and the reference check must agree
// or it would let the delete through and break the button.
func TestAdminSoundsDeleteIsCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("this host's filesystem is case-sensitive")
	}
	ts := newTestServer(t, &recordingInput{})
	if code, body := uploadSound(t, ts, "boom.wav", wavBytes()); code != http.StatusCreated {
		t.Fatalf("upload: %d %s", code, body)
	}

	code, body := adminDo(t, ts, http.MethodGet, "/api/v1/admin/profiles/development/document", "")
	if code != http.StatusOK {
		t.Fatalf("GET document: %d %s", code, body)
	}
	var doc profile.Profile
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	// The profile references the lower-case name; the delete uses a different
	// case of the same file.
	doc.Pages[0].Buttons[0].OnPress = &profile.Action{
		Type:   "sound.play",
		Params: json.RawMessage(`{"file":"boom.wav"}`),
	}
	updated, _ := json.Marshal(doc)
	if code, body := adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/development/document", string(updated)); code != http.StatusOK {
		t.Fatalf("PUT document: %d %s", code, body)
	}

	if code, body := adminDo(t, ts, http.MethodDelete, "/api/v1/admin/sounds/BOOM.WAV", ""); code != http.StatusConflict {
		t.Fatalf("DELETE with a different case returned %d, want 409: %s", code, body)
	}
}
