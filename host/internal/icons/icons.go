// Package icons fetches, rasterises and caches the icons a profile references.
//
// Why this exists: a profile may use `"icon": {"type":"image","value":"terminal.png"}`,
// but protocol v1 has no message that fetches a file from the host, so an image
// icon used to render as a placeholder. The fix chosen is to inline the icons a
// profile actually references into the profile document as data URIs
// (docs/PROTOCOL.md §5). That keeps one message, needs no new endpoint, needs no
// authentication decision, and lets two buttons share one icon.
//
// Two consequences shape this package:
//
//   - The document grows, so only referenced icons are inlined and each one is
//     rasterised to a fixed 128x128 PNG. A profile with thirty icons adds a few
//     hundred kilobytes, not a few megabytes.
//   - Fetching needs the internet, which the project otherwise does not. So the
//     cache is on disk, a miss is never fatal, and an icon that cannot be
//     obtained is simply absent from the document and the client draws its
//     placeholder. The deck works with no network; the picker is what needs one.
package icons

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// Size is the edge of the PNG written into a profile. A phone tile is drawn at
// roughly 96 to 140 logical pixels depending on density, so 128 covers the
// largest case without shipping pixels nobody sees.
const Size = 128

// maxSVGBytes bounds a downloaded icon. A real Lucide or Material icon is under
// 2 KiB; anything larger is not an icon.
const maxSVGBytes = 256 << 10

// maxCatalogBytes bounds a downloaded icon-set catalogue.
const maxCatalogBytes = 8 << 20

// Set identifies an icon source.
//
// A set is either a GitHub repository of plain SVG files or an Iconify
// collection. Both are open and need no key, which is what makes the picker work
// without the user registering anywhere (see docs/ICONS.md).
type Set struct {
	// ID is the short name used in the cache path and in the API.
	ID string
	// Name is what the picker shows.
	Name string
	// Licence is the upstream licence, shown in the picker so a user can see
	// what they are vendoring into their own profile.
	Licence string
	// Homepage is where the icons come from.
	Homepage string

	// Iconify is the collection prefix when the set comes from api.iconify.design.
	// Empty means GitHub.
	Iconify string
	// Repo is "owner/name" for a GitHub set.
	Repo string
	// Dir is the directory inside the repository that holds the SVGs.
	Dir string
	// Branch is the branch to read.
	Branch string
	// Catalog is the path to a JSON file listing the icon names, for a GitHub
	// set that has no API listing. Unused when Iconify is set.
	Catalog string
}

// Sets is the built-in catalogue of icon sources.
//
// Every one of them is permissively licensed and usable offline once cached. The
// list is deliberately short: each entry is a promise that the fetch works and
// that the licence is clean.
var Sets = []Set{
	{
		ID: "lucide", Name: "Lucide", Licence: "ISC",
		Homepage: "https://lucide.dev", Iconify: "lucide",
	},
	{
		ID: "material-symbols", Name: "Material Symbols", Licence: "Apache-2.0",
		Homepage: "https://fonts.google.com/icons", Iconify: "material-symbols",
	},
	{
		ID: "tabler", Name: "Tabler Icons", Licence: "MIT",
		Homepage: "https://tabler.io/icons", Iconify: "tabler",
	},
	{
		ID: "bootstrap", Name: "Bootstrap Icons", Licence: "MIT",
		Homepage: "https://icons.getbootstrap.com", Iconify: "bi",
	},
	{
		ID: "phosphor", Name: "Phosphor", Licence: "MIT",
		Homepage: "https://phosphoricons.com", Iconify: "ph",
	},
}

// SetByID finds a set.
func SetByID(id string) (Set, bool) {
	for _, s := range Sets {
		if s.ID == id {
			return s, true
		}
	}
	return Set{}, false
}

// Result is one fetched icon.
type Result struct {
	// FileName is what goes into `icon.value`, e.g. "terminal.png".
	FileName string
	// DataURI is the base64 PNG the client renders.
	DataURI string
	// Source records where it came from, for the profile's own bookkeeping.
	Source string
	// Bytes is the size of the PNG.
	Bytes int
}

// Client fetches icons and keeps a disk cache.
type Client struct {
	dir  string
	log  *slog.Logger
	http *http.Client
	now  func() time.Time

	mu     sync.Mutex
	flight map[string]*sync.WaitGroup // in-flight downloads, so a burst of
	// requests for the same icon downloads it once
}

// New creates a client whose cache lives in dir.
func New(dir string, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("icons: create cache %s: %w", dir, err)
	}
	return &Client{
		dir: dir,
		log: log,
		http: &http.Client{
			// A download that hangs must not hold up a profile save, which is
			// what this whole path serves.
			Timeout: 15 * time.Second,
		},
		now:    time.Now,
		flight: map[string]*sync.WaitGroup{},
	}, nil
}

// CacheDir is where the PNGs live, for the CLI's diagnostics.
func (c *Client) CacheDir() string { return c.dir }

// Catalog lists the icon names available in a set, sorted.
//
// It is cached on disk for a day: the catalogue of a released icon set changes
// rarely, and asking Iconify on every keystroke in the picker would be rude and
// slow. A stale catalogue is harmless, because fetching an icon that no longer
// exists fails cleanly.
func (c *Client) Catalog(ctx context.Context, set Set) ([]string, error) {
	cachePath := filepath.Join(c.dir, "catalog-"+set.ID+".json")
	if names, ok := readCatalog(cachePath); ok && c.now().Sub(fileModTime(cachePath)) < 24*time.Hour {
		return names, nil
	}

	var names []string
	var err error
	if set.Iconify != "" {
		names, err = c.fetchIconifyCatalog(ctx, set)
	} else {
		names, err = c.fetchGitHubCatalog(ctx, set)
	}
	if err != nil {
		// A stale catalogue is better than none when the network is down.
		if cached, ok := readCatalog(cachePath); ok {
			c.log.Warn("icon catalogue could not be refreshed; using the cached copy",
				"set", set.ID, "error", err)
			return cached, nil
		}
		return nil, err
	}

	sort.Strings(names)
	if b, err := json.Marshal(names); err == nil {
		_ = os.WriteFile(cachePath, b, 0o600)
	}
	return names, nil
}

func (c *Client) fetchIconifyCatalog(ctx context.Context, set Set) ([]string, error) {
	url := "https://api.iconify.design/collection?prefix=" + set.Iconify
	body, err := c.get(ctx, url, maxCatalogBytes)
	if err != nil {
		return nil, fmt.Errorf("icons: catalogue for %s: %w", set.ID, err)
	}

	// The response groups names by category, with the uncategorised ones in
	// their own array. Both shapes are collected so nothing is missed.
	var doc struct {
		Uncategorized []string            `json:"uncategorized"`
		Categories    map[string][]string `json:"categories"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("icons: catalogue for %s: %w", set.ID, err)
	}
	seen := map[string]bool{}
	var out []string
	add := func(list []string) {
		for _, n := range list {
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	add(doc.Uncategorized)
	for _, list := range doc.Categories {
		add(list)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("icons: catalogue for %s is empty", set.ID)
	}
	return out, nil
}

func (c *Client) fetchGitHubCatalog(ctx context.Context, set Set) ([]string, error) {
	// GitHub has no cheap "list this directory" API that does not need a token,
	// so a set served this way must ship a catalogue file. A single JSON array
	// of names is enough and costs one request.
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", set.Repo, set.Branch, set.Catalog)
	body, err := c.get(ctx, url, maxCatalogBytes)
	if err != nil {
		return nil, fmt.Errorf("icons: catalogue for %s: %w", set.ID, err)
	}
	var names []string
	if err := json.Unmarshal(body, &names); err != nil {
		return nil, fmt.Errorf("icons: catalogue for %s: %w", set.ID, err)
	}
	return names, nil
}

// Get returns the icon, fetching and rasterising it if the cache does not have
// it. The name is an icon name in the set ("terminal", "arrow-right"), not a
// file name.
func (c *Client) Get(ctx context.Context, set Set, name string) (Result, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Result{}, fmt.Errorf("icons: name must not be empty")
	}
	if !validIconName(name) {
		return Result{}, fmt.Errorf("icons: %q is not a valid icon name", name)
	}

	fileName := fileNameFor(set.ID, name)
	target := filepath.Join(c.dir, fileName)

	// The cache is the fast path and the offline path.
	if b, err := os.ReadFile(target); err == nil && len(b) > 0 {
		return result(fileName, b, set.ID), nil
	}

	// One download per icon, however many callers ask at once. The picker
	// previews icons as the user types, so duplicates are the normal case.
	release := c.claim(set.ID + "/" + name)
	defer release()

	if b, err := os.ReadFile(target); err == nil && len(b) > 0 {
		return result(fileName, b, set.ID), nil
	}

	svg, err := c.fetchSVG(ctx, set, name)
	if err != nil {
		return Result{}, err
	}
	pngBytes, err := Rasterize(svg, Size)
	if err != nil {
		return Result{}, fmt.Errorf("icons: rendering %s/%s: %w", set.ID, name, err)
	}
	// Written atomically, because two saves of different profiles can race here
	// and a half-written PNG would be served forever afterwards.
	if err := writeAtomic(target, pngBytes); err != nil {
		c.log.Warn("icon was rendered but could not be cached", "icon", fileName, "error", err)
	}
	return result(fileName, pngBytes, set.ID), nil
}

// claim serialises concurrent downloads of the same icon.
func (c *Client) claim(key string) func() {
	c.mu.Lock()
	if wg, ok := c.flight[key]; ok {
		c.mu.Unlock()
		wg.Wait()
		// The winner has finished; the caller re-reads the cache.
		return func() {}
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	c.flight[key] = wg
	c.mu.Unlock()

	return func() {
		c.mu.Lock()
		delete(c.flight, key)
		c.mu.Unlock()
		wg.Done()
	}
}

func (c *Client) fetchSVG(ctx context.Context, set Set, name string) ([]byte, error) {
	var url string
	if set.Iconify != "" {
		// Iconify serves one SVG per icon, already sized and with real colours,
		// which avoids the `currentColor` problem described in Rasterize.
		url = fmt.Sprintf("https://api.iconify.design/%s/%s.svg?height=%d", set.Iconify, name, Size)
	} else {
		url = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s.svg",
			set.Repo, set.Branch, set.Dir, name)
	}
	body, err := c.get(ctx, url, maxSVGBytes)
	if err != nil {
		return nil, fmt.Errorf("icons: fetching %s/%s: %w", set.ID, name, err)
	}
	if !bytes.Contains(body, []byte("<svg")) {
		return nil, fmt.Errorf("icons: %s/%s did not return an SVG", set.ID, name)
	}
	return body, nil
}

func (c *Client) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MobileDeck/0.1 (+https://github.com/mobiledeck/mobiledeck)")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// Rasterize renders an SVG to a PNG of the given edge length.
//
// Two upstream quirks are handled here, both found by trying a real Lucide icon:
//
//   - `oksvg` rejects the CSS keyword `currentColor`, which every Lucide and
//     Feather icon uses for its stroke, with the unhelpful error "param
//     mismatch". It is substituted with the requested colour before parsing.
//   - A stroke-only icon (`fill="none"`) has no filled paths, so the rasteriser
//     has nothing to draw unless the SVG carries stroke attributes. Iconify
//     serves icons already expanded, so this only matters for a raw GitHub
//     source; the empty-output check below turns it into an error rather than a
//     silently blank icon.
func Rasterize(svg []byte, size int) ([]byte, error) {
	if size < 16 || size > 1024 {
		return nil, fmt.Errorf("icons: size %d is outside 16..1024", size)
	}

	// Substitute the CSS keyword before parsing. The colour is arbitrary: the
	// client tints nothing, and the icons are drawn on a dark tile.
	prepared := bytes.ReplaceAll(svg, []byte("currentColor"), []byte("#e6e6ea"))

	icon, err := oksvg.ReadIconStream(bytes.NewReader(prepared))
	if err != nil {
		return nil, fmt.Errorf("icons: parsing SVG: %w", err)
	}
	icon.SetTarget(0, 0, float64(size), float64(size))

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	scanner := rasterx.NewScannerGV(size, size, img, img.Bounds())
	raster := rasterx.NewDasher(size, size, scanner)
	icon.Draw(raster, 1.0)

	if !hasVisiblePixels(img) {
		return nil, fmt.Errorf("icons: the SVG rendered empty; it is probably stroke-only and needs stroke attributes")
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("icons: encoding PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// hasVisiblePixels reports whether anything with opacity was drawn.
func hasVisiblePixels(img *image.RGBA) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				return true
			}
		}
	}
	return false
}

// DataURI wraps a PNG in the base64 data URI the protocol uses.
func DataURI(pngBytes []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
}

// Resolve returns the data URI for a file name, or "" when it is not cached.
// It never fetches: it is what the profile encoder calls on the hot path.
func (c *Client) Resolve(fileName string) string {
	b, err := os.ReadFile(filepath.Join(c.dir, sanitizeFileName(fileName)))
	if err != nil || len(b) == 0 {
		return ""
	}
	return DataURI(b)
}

// Import copies an image file the user chose into the cache, converting it to
// the same PNG form as everything else so the client has one thing to decode.
func (c *Client) Import(srcPath string) (Result, error) {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return Result{}, fmt.Errorf("icons: reading %s: %w", srcPath, err)
	}
	if len(raw) > 4<<20 {
		return Result{}, fmt.Errorf("icons: %s is larger than 4 MiB", srcPath)
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Result{}, fmt.Errorf("icons: %s is not a readable image: %w", srcPath, err)
	}
	// Re-encoded rather than copied: a 2000x2000 JPEG from a phone camera must
	// not end up base64'd into a profile document.
	resized := fitSquare(img, Size)
	var buf bytes.Buffer
	if err := png.Encode(&buf, resized); err != nil {
		return Result{}, fmt.Errorf("icons: encoding %s: %w", srcPath, err)
	}

	fileName := customFileName(filepath.Base(srcPath))
	if err := writeAtomic(filepath.Join(c.dir, fileName), buf.Bytes()); err != nil {
		return Result{}, err
	}
	return result(fileName, buf.Bytes(), "custom"), nil
}

// fitSquare scales an image to a size x size square, preserving aspect ratio and
// padding with transparency. Nearest-neighbour is enough here: the target is
// small and the source is a user's icon, not a photograph.
func fitSquare(src image.Image, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return dst
	}
	scale := float64(size) / float64(sw)
	if s := float64(size) / float64(sh); s < scale {
		scale = s
	}
	dw := int(float64(sw) * scale)
	dh := int(float64(sh) * scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	ox := (size - dw) / 2
	oy := (size - dh) / 2

	for y := range dh {
		sy := b.Min.Y + y*sh/dh
		for x := range dw {
			sx := b.Min.X + x*sw/dw
			r, g, bl, a := src.At(sx, sy).RGBA()
			dst.Set(ox+x, oy+y, color.RGBA{
				R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: uint8(a >> 8),
			})
		}
	}
	return dst
}

// --- helpers -------------------------------------------------------------

func result(fileName string, pngBytes []byte, source string) Result {
	return Result{
		FileName: fileName,
		DataURI:  DataURI(pngBytes),
		Source:   source,
		Bytes:    len(pngBytes),
	}
}

// fileNameFor names a built-in icon's cache file. The set is part of the name so
// two sets may each have a "home" icon without colliding.
func fileNameFor(setID, name string) string {
	return setID + "--" + sanitizeFileName(name) + ".png"
}

// customFileName names an imported file, keeping a readable stem and adding a
// short digest so two files called "logo.png" do not overwrite each other.
func customFileName(base string) string {
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	sum := sha256.Sum256([]byte(base + time.Now().String()))
	return "custom--" + sanitizeFileName(stem) + "-" + hex.EncodeToString(sum[:4]) + ".png"
}

// sanitizeFileName keeps a name safe to use as a file name. Icon names come from
// a remote catalogue, so they are untrusted input as far as this is concerned.
func sanitizeFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		out = "icon"
	}
	return out
}

// validIconName accepts the shape both sources use: lower case words separated
// by dashes or digits, e.g. "terminal", "arrow-right", "volume-2".
func validIconName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.'
		if !ok {
			return false
		}
	}
	return true
}

func readCatalog(path string) ([]string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil || len(names) == 0 {
		return nil, false
	}
	return names, true
}

func fileModTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// writeAtomic writes through a temporary file in the same directory, so a
// reader never sees a partial PNG.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".icon-*.tmp")
	if err != nil {
		return fmt.Errorf("icons: temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("icons: writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("icons: closing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("icons: rename to %s: %w", path, err)
	}
	return nil
}

// CachedInSet lists the icon names of one set that are already on disk, so the
// picker can mark what works without a network round trip.
func (c *Client) CachedInSet(setID string) []string {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil
	}
	prefix := setID + "--"
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".png") {
			continue
		}
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".png"))
	}
	sort.Strings(out)
	return out
}

// WriteImported stores an image the user supplied and returns the file name the
// profile should reference.
//
// The bytes are re-encoded to the same 128x128 PNG as every other icon, so the
// client has exactly one thing to decode and a profile cannot be inflated by a
// multi-megabyte photograph. The stored name is derived from the supplied name
// and sanitised: it becomes a path component, so it is untrusted input.
func (c *Client) WriteImported(name string, raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("icons: the image is empty")
	}
	if len(raw) > 4<<20 {
		return "", fmt.Errorf("icons: the image is larger than 4 MiB")
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("icons: not a readable image: %w", err)
	}
	resized := fitSquare(img, Size)
	var buf bytes.Buffer
	if err := png.Encode(&buf, resized); err != nil {
		return "", fmt.Errorf("icons: encoding the image: %w", err)
	}

	fileName := customFileName(name)
	if err := writeAtomic(filepath.Join(c.dir, fileName), buf.Bytes()); err != nil {
		return "", err
	}
	return fileName, nil
}
