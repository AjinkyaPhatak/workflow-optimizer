package application

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/files"
	"workflow-optimizer/internal/workspace"
)

// FileService authorizes workspace file management (Phase D1): members who
// may edit workflows upload and delete files; any member lists and
// downloads them.
type FileService struct {
	access *Access
	files  *files.Service
}

// NewFileService wires the service.
func NewFileService(access *Access, svc *files.Service) *FileService {
	return &FileService{access: access, files: svc}
}

func fileError(err error) error {
	switch {
	case errors.Is(err, files.ErrNotFound):
		return notFound("file")
	case errors.Is(err, files.ErrInvalid):
		return &InvalidError{Message: strings.TrimPrefix(err.Error(), files.ErrInvalid.Error()+": ")}
	}
	return err
}

// Upload stores a file in the workspace.
func (s *FileService) Upload(ctx context.Context, user, workspaceID uuid.UUID, name string, data []byte) (files.File, error) {
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionWrite, "workspace"); err != nil {
		return files.File{}, err
	}
	f, err := s.files.Upload(ctx, workspaceID, user, name, data)
	return f, fileError(err)
}

// List returns the workspace's files.
func (s *FileService) List(ctx context.Context, user, workspaceID uuid.UUID) ([]files.File, error) {
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionRead, "workspace"); err != nil {
		return nil, err
	}
	out, err := s.files.Repository().List(ctx, workspaceID)
	return out, fileError(err)
}

// locate finds a file and checks the caller may act on it; a file of a
// workspace the caller does not belong to is not found.
func (s *FileService) locate(ctx context.Context, user, id uuid.UUID, action workspace.Action) (files.File, error) {
	f, err := s.files.Repository().Locate(ctx, id)
	if err != nil {
		return files.File{}, fileError(err)
	}
	if _, err := s.access.Require(ctx, user, f.WorkspaceID, action, "file"); err != nil {
		return files.File{}, err
	}
	return f, nil
}

// Get returns a file's metadata.
func (s *FileService) Get(ctx context.Context, user, id uuid.UUID) (files.File, error) {
	return s.locate(ctx, user, id, workspace.ActionRead)
}

// Download returns a file with its bytes.
func (s *FileService) Download(ctx context.Context, user, id uuid.UUID) (files.File, []byte, error) {
	f, err := s.locate(ctx, user, id, workspace.ActionRead)
	if err != nil {
		return files.File{}, nil, err
	}
	f, data, err := s.files.Repository().Content(ctx, f.WorkspaceID, id)
	return f, data, fileError(err)
}

// Delete removes a file. Workflows that reference it fail when they run.
func (s *FileService) Delete(ctx context.Context, user, id uuid.UUID) error {
	f, err := s.locate(ctx, user, id, workspace.ActionWrite)
	if err != nil {
		return err
	}
	return fileError(s.files.Repository().Delete(ctx, f.WorkspaceID, id))
}
