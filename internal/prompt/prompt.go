// Package prompt is the prompt store (plan.md §3④). Prompts live outside the
// reviewed repository; the filesystem backend reads versioned prompts from the
// Momos deployment. Prompts are Go text/templates rendered with the
// ReviewEvent, and each render records a prompt version (path@contenthash) for
// reproducibility and the evaluation dataset.
package prompt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Hades-Scheduler/momos/internal/event"
)

// Rendered is a prompt rendered for a specific event.
type Rendered struct {
	Text    string // the final prompt text
	Version string // "prompts/go-backend-review.md@a91f0c3d1e2f"
}

// Store resolves and renders prompts.
type Store interface {
	Render(ref string, ev *event.ReviewEvent) (*Rendered, error)
}

// FilesystemStore reads prompts from a root directory. A ref is a path relative
// to the root (e.g. "prompts/go-backend-review.md").
type FilesystemStore struct {
	root string
}

// NewFilesystemStore returns a store rooted at dir.
func NewFilesystemStore(dir string) *FilesystemStore {
	return &FilesystemStore{root: dir}
}

// Render loads the prompt at ref, renders it with the event, and computes its
// version.
func (s *FilesystemStore) Render(ref string, ev *event.ReviewEvent) (*Rendered, error) {
	clean := filepath.Clean(ref)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return nil, fmt.Errorf("invalid prompt ref %q", ref)
	}
	path := filepath.Join(s.root, clean)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read prompt %q: %w", ref, err)
	}
	sum := sha256.Sum256(raw)
	version := fmt.Sprintf("%s@%s", ref, hex.EncodeToString(sum[:])[:12])

	tmpl, err := template.New(ref).Option("missingkey=zero").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse prompt template %q: %w", ref, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ev); err != nil {
		return nil, fmt.Errorf("render prompt %q: %w", ref, err)
	}
	return &Rendered{Text: buf.String(), Version: version}, nil
}
