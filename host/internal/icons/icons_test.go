package icons

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A filled triangle, which needs no stroke support to render.
const filledSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24"><path d="M12 2L2 22h20z" fill="#4c8bf5"/></svg>`

// The shape Lucide actually serves: stroke-only, with the CSS keyword
// currentColor. This is the case that failed with "param mismatch".
const strokeSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 17l6-6-6-6"/><path d="M12 19h8"/></svg>`

func decode(t *testing.T, pngBytes []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("the output is not a decodable PNG: %v", err)
	}
	return img
}

func TestRasterizeFilledSVG(t *testing.T) {
	out, err := Rasterize([]byte(filledSVG), 64)
	if err != nil {
		t.Fatalf("Rasterize: %v", err)
	}
	img := decode(t, out)
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 64 {
		t.Fatalf("size = %v, want 64x64", b)
	}
}

// TestRasterizeStrokeSVGWithCurrentColor is the regression test for the bug that
// made every Lucide icon fail: oksvg rejects the CSS keyword `currentColor` with
// "param mismatch", and every Lucide and Feather icon uses it.
func TestRasterizeStrokeSVGWithCurrentColor(t *testing.T) {
	out, err := Rasterize([]byte(strokeSVG), 96)
	if err != nil {
		t.Fatalf("a stroke-only icon with currentColor must render: %v", err)
	}
	img := decode(t, out)

	// It must not be blank: a stroke-only SVG rendered without stroke support
	// produces an empty image, which is the silent failure worth catching.
	visible := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				visible++
			}
		}
	}
	if visible < 50 {
		t.Fatalf("the stroke icon rendered %d visible pixels; it is effectively blank", visible)
	}
}

func TestRasterizeRejectsEmptySVG(t *testing.T) {
	// A well-formed SVG with nothing to draw must be an error, not a blank icon.
	_, err := Rasterize([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"></svg>`), 64)
	if err == nil {
		t.Fatal("an SVG that renders nothing was accepted")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRasterizeRejectsGarbage(t *testing.T) {
	if _, err := Rasterize([]byte("not an svg at all"), 64); err == nil {
		t.Fatal("garbage was accepted")
	}
}

func TestRasterizeBoundsSize(t *testing.T) {
	for _, size := range []int{0, 8, 4096} {
		if _, err := Rasterize([]byte(filledSVG), size); err == nil {
			t.Errorf("size %d was accepted", size)
		}
	}
}

func TestDataURI(t *testing.T) {
	uri := DataURI([]byte{1, 2, 3})
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("prefix = %q", uri)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("the payload is not valid base64: %v", err)
	}
	if !bytes.Equal(raw, []byte{1, 2, 3}) {
		t.Fatalf("round trip = %v", raw)
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"terminal":         "terminal",
		"arrow-right":      "arrow-right",
		"../../etc/passwd": "passwd",
		"a/b":              "b",
		"":                 "icon",
		"..":               "icon",
		"we ird*name":      "we-ird-name",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidIconName(t *testing.T) {
	for _, ok := range []string{"terminal", "arrow-right", "volume-2", "a.b"} {
		if !validIconName(ok) {
			t.Errorf("%q was rejected", ok)
		}
	}
	for _, bad := range []string{"", "UPPER", "../x", "a/b", "a b", strings.Repeat("x", 65)} {
		if validIconName(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestSetByID(t *testing.T) {
	if _, ok := SetByID("lucide"); !ok {
		t.Error("lucide is missing from the built-in sets")
	}
	if _, ok := SetByID("nope"); ok {
		t.Error("an unknown set was found")
	}
	// Every set must carry its licence, because the picker shows it and a user
	// vendoring an icon into their own profile needs to know the terms.
	for _, s := range Sets {
		if s.Licence == "" {
			t.Errorf("set %q has no licence", s.ID)
		}
		if s.Iconify == "" && (s.Repo == "" || s.Catalog == "") {
			t.Errorf("set %q has no way to fetch a catalogue", s.ID)
		}
	}
}

func TestResolveFromCache(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Absent: no URI, and no error.
	if uri := c.Resolve("terminal.png"); uri != "" {
		t.Fatalf("Resolve on a missing icon = %q, want empty", uri)
	}

	// Present: a data URI that decodes.
	pngBytes, err := Rasterize([]byte(filledSVG), 64)
	if err != nil {
		t.Fatalf("Rasterize: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lucide--terminal.png"), pngBytes, 0o600); err != nil {
		t.Fatalf("writing the cache entry: %v", err)
	}
	uri := c.Resolve("lucide--terminal.png")
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("Resolve = %q", uri)
	}
}

// TestImportResizesAndEncodes covers a user's own image: it must be normalised to
// the same PNG form, not copied at its original size.
func TestImportResizesAndEncodes(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// A large, non-square source image.
	src := image.NewRGBA(image.Rect(0, 0, 800, 200))
	for y := range 200 {
		for x := range 800 {
			src.Set(x, y, image.White)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("encoding the source: %v", err)
	}
	srcPath := filepath.Join(dir, "wide logo.png")
	if err := os.WriteFile(srcPath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("writing the source: %v", err)
	}

	res, err := c.Import(srcPath)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !strings.HasSuffix(res.FileName, ".png") {
		t.Errorf("file name = %q, want a .png", res.FileName)
	}
	if !strings.Contains(res.FileName, "wide") {
		t.Errorf("file name = %q, want it to keep the source stem", res.FileName)
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(res.DataURI, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("the data URI is not valid base64: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the imported icon is not a PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != Size || b.Dy() != Size {
		t.Fatalf("imported size = %v, want %dx%d", b, Size, Size)
	}
}

func TestImportRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	c, _ := New(dir, nil)
	bad := filepath.Join(dir, "not-an-image.txt")
	_ = os.WriteFile(bad, []byte("hello"), 0o600)
	if _, err := c.Import(bad); err == nil {
		t.Fatal("a text file was imported as an icon")
	}
}

func TestGetRejectsBadNames(t *testing.T) {
	c, _ := New(t.TempDir(), nil)
	set, _ := SetByID("lucide")
	ctx := context.Background()

	for _, bad := range []string{"", "  ", "../etc/passwd", "UPPER", "a/b"} {
		if _, err := c.Get(ctx, set, bad); err == nil {
			t.Errorf("Get accepted %q", bad)
		}
	}
}
