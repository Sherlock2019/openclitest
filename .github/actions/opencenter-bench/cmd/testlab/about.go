package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// The "OpenCenter : the big picture" panel: the digital-city diagram and the
// table that goes with it.
//
// The table is data rather than markup because it is the same metaphor the
// command table's "like building a city" column uses, and two copies of a
// metaphor drift. Keeping it in config/ means they can be compared.

// Overview is config/what-is-opencenter.yaml.
type Overview struct {
	Title      string      `yaml:"title"      json:"title"`
	Lead       string      `yaml:"lead"       json:"lead"`
	Image      []string    `yaml:"image"      json:"-"`
	ImageAlt   string      `yaml:"image_alt"  json:"image_alt"`
	Components []Component `yaml:"components" json:"components"`
	// ImageURL is the route the page loads, set only when a file was found.
	// Empty means the panel shows the table alone rather than a broken image.
	ImageURL string `yaml:"-" json:"image_url,omitempty"`
}

// Component is one row: what it is, what it is like, what it does.
type Component struct {
	Number   int    `yaml:"n"        json:"n"`
	Name     string `yaml:"name"     json:"name"`
	Metaphor string `yaml:"metaphor" json:"metaphor"`
	Role     string `yaml:"role"     json:"role"`
}

// loadOverview reads the panel, and locates the diagram if it is there.
//
// A missing file is not an error. The bench does not ship the picture — it is
// an illustration somebody drops in — and a console that refused to start
// without it would be holding the whole page hostage to an optional image.
func loadOverview(root string) *Overview {
	raw, err := os.ReadFile(filepath.Join(root, "config", "what-is-opencenter.yaml"))
	if err != nil {
		return nil
	}
	var overview Overview
	if err := yaml.Unmarshal(raw, &overview); err != nil {
		return nil
	}
	for _, candidate := range overview.Image {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(candidate))); err == nil {
			overview.ImageURL = "/assets/city"
			break
		}
	}
	return &overview
}

// handleAsset serves the diagram.
//
// It serves exactly the candidates the configuration names and nothing else.
// The obvious alternative — a path parameter under a directory — is a file
// server reachable without credentials, and every one of those is a traversal
// bug waiting for someone to write it slightly wrong. There is one picture, so
// there is one route and no parameter to get wrong.
func (c *console) handleAsset(w http.ResponseWriter, r *http.Request) {
	if c.overview == nil {
		http.NotFound(w, r)
		return
	}
	for _, candidate := range c.overview.Image {
		path := filepath.Join(c.root, filepath.FromSlash(candidate))
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		w.Header().Set("Content-Type", contentTypeFor(candidate))
		// Long enough to stop a re-fetch on every render, short enough that
		// replacing the picture shows up without an explanation.
		w.Header().Set("Cache-Control", "public, max-age=300")
		http.ServeFile(w, r, path)
		return
	}
	http.NotFound(w, r)
}

// maxImageBytes caps an upload. Generous for a diagram, finite because the
// endpoint writes to disk and an unbounded POST is a way to fill it.
const maxImageBytes = 16 << 20

// handleAssetUpload replaces the diagram from the page.
//
// The filename is fixed, not taken from the upload. A caller supplies bytes and
// nothing else — no name, no path, no extension — so there is nothing to
// traverse with and nothing to overwrite except the one file this route owns.
//
// The bytes are sniffed rather than trusted: a PNG, JPEG or WebP magic number,
// or it is refused. A Content-Type header is a claim by the sender and an HTML
// file announcing itself as an image would otherwise be written into the
// repository and served back.
func (c *console) handleAssetUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if c.overview == nil || len(c.overview.Image) == 0 {
		http.Error(w, "no image slot is configured", http.StatusNotFound)
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxImageBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "the upload is too large or could not be read", http.StatusRequestEntityTooLarge)
		return
	}
	kind := sniffImage(data)
	if kind == "" {
		http.Error(w, "that is not a PNG, JPEG or WebP", http.StatusUnsupportedMediaType)
		return
	}

	// Always the first configured path, whatever was uploaded. One file, one
	// name, decided by the server.
	destination := filepath.Join(c.root, filepath.FromSlash(c.overview.Image[0]))
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		http.Error(w, "could not create the docs directory", http.StatusInternalServerError)
		return
	}
	// Written beside the destination and renamed, so an interrupted upload
	// leaves the previous image in place rather than a truncated one.
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".upload-*")
	if err != nil {
		http.Error(w, "could not write the image", http.StatusInternalServerError)
		return
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		http.Error(w, "could not write the image", http.StatusInternalServerError)
		return
	}
	if err := temporary.Close(); err != nil {
		http.Error(w, "could not write the image", http.StatusInternalServerError)
		return
	}
	if err := os.Chmod(name, 0o644); err != nil {
		http.Error(w, "could not set the image mode", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(name, destination); err != nil {
		http.Error(w, "could not replace the image", http.StatusInternalServerError)
		return
	}

	// The panel was told there was no image at startup if the file was absent.
	// Say there is one now, so the page can show it without a restart.
	c.mu.Lock()
	c.overview.ImageURL = "/assets/city"
	c.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "saved", "bytes": len(data), "kind": kind,
		"path": c.overview.Image[0],
	})
}

// sniffImage identifies an image by its first bytes.
func sniffImage(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "jpeg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	}
	return ""
}

func contentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".svg":
		// Served as a picture, not as a document. An SVG rendered as
		// image/svg+xml in a top-level context can carry script; as an <img>
		// source it cannot, and this route only ever feeds an <img>.
		return "image/svg+xml"
	}
	return "application/octet-stream"
}
