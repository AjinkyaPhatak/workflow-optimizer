// Package files is workspace file storage (Phase D1): documents and images
// uploaded to a workspace, read by workflow nodes (Extract Text, the LLM
// node's images, knowledge bases) by file ID. Files belong to one workspace;
// every read names that workspace, so a node can never read another
// workspace's file.
package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// MaxFileSize bounds one file (20 MB).
const MaxFileSize = 20 << 20

// MaxNameLength bounds a file name.
const MaxNameLength = 255

var (
	// ErrNotFound: no such file in the workspace.
	ErrNotFound = errors.New("files: not found")
	// ErrInvalid: the upload is unusable (empty, too large, bad name).
	ErrInvalid = errors.New("files: invalid")
)

// File is a stored file's metadata.
type File struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Name        string
	ContentType string
	Size        int64
	SHA256      string
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
}

// Repository stores files with their content.
type Repository interface {
	Create(ctx context.Context, f File, data []byte) (File, error)
	// Get returns a file of the workspace (ErrNotFound otherwise).
	Get(ctx context.Context, workspaceID, id uuid.UUID) (File, error)
	// Content returns a file of the workspace with its bytes.
	Content(ctx context.Context, workspaceID, id uuid.UUID) (File, []byte, error)
	// Locate finds a file by ID alone (to authorize by its workspace).
	Locate(ctx context.Context, id uuid.UUID) (File, error)
	List(ctx context.Context, workspaceID uuid.UUID) ([]File, error)
	Delete(ctx context.Context, workspaceID, id uuid.UUID) error
}

// Reader is what nodes use: a workspace's file with its bytes.
type Reader interface {
	Content(ctx context.Context, workspaceID, id uuid.UUID) (File, []byte, error)
	List(ctx context.Context, workspaceID uuid.UUID) ([]File, error)
}

// extensionTypes are the types recognized by extension, which wins over
// content sniffing for formats that sniff as generic zip or text.
var extensionTypes = map[string]string{
	".pdf": "application/pdf", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".txt": "text/plain", ".md": "text/markdown", ".markdown": "text/markdown", ".csv": "text/csv", ".tsv": "text/tab-separated-values",
	".json": "application/json", ".html": "text/html", ".htm": "text/html", ".xml": "application/xml",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
}

// ContentType decides a file's type from its extension, else its bytes.
func ContentType(name string, data []byte) string {
	if t, ok := extensionTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return t
	}
	t := http.DetectContentType(data)
	if mt, _, err := mime.ParseMediaType(t); err == nil {
		return mt
	}
	return "application/octet-stream"
}

// CleanName returns a safe display name: no path, no control characters,
// at most MaxNameLength bytes.
func CleanName(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 && r != 0x7f && r != utf8.RuneError {
			b.WriteRune(r)
		}
	}
	name = strings.TrimSpace(b.String())
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("%w: the file needs a name", ErrInvalid)
	}
	for len(name) > MaxNameLength {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name, nil
}

// Service validates and stores uploads.
type Service struct {
	repo Repository
}

// NewService wraps the repository.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Repository is the underlying store.
func (s *Service) Repository() Repository { return s.repo }

// Upload stores a file in a workspace.
func (s *Service) Upload(ctx context.Context, workspaceID, user uuid.UUID, name string, data []byte) (File, error) {
	clean, err := CleanName(name)
	if err != nil {
		return File{}, err
	}
	switch {
	case len(data) == 0:
		return File{}, fmt.Errorf("%w: the file is empty", ErrInvalid)
	case len(data) > MaxFileSize:
		return File{}, fmt.Errorf("%w: the file is larger than %d MB", ErrInvalid, MaxFileSize>>20)
	}
	sum := sha256.Sum256(data)
	return s.repo.Create(ctx, File{ID: uuid.New(), WorkspaceID: workspaceID, Name: clean, ContentType: ContentType(clean, data),
		Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), CreatedBy: user}, data)
}

// IsImage reports whether a content type is an image the LLM node accepts.
func IsImage(contentType string) bool {
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}
