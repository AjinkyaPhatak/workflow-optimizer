package handlers

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/responses"
	"workflow-optimizer/internal/files"
)

// maxUploadBytes bounds an upload request (the file plus form overhead).
const maxUploadBytes = files.MaxFileSize + 1<<20

// UploadFile handles POST /api/v1/files: multipart/form-data with a
// workspace_id field and one file part named "file" (Phase D1).
func (h *Handlers) UploadFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		h.fail(w, r, httpx.BadRequest("the upload must be multipart/form-data"))
		return
	}
	var (
		ws       uuid.UUID
		name     string
		data     []byte
		haveFile bool
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			h.fail(w, r, uploadError(err))
			return
		}
		switch part.FormName() {
		case "workspace_id":
			b, _ := io.ReadAll(io.LimitReader(part, 100))
			if ws, err = uuid.Parse(strings.TrimSpace(string(b))); err != nil {
				h.fail(w, r, httpx.BadRequest("workspace_id must be a UUID"))
				return
			}
		case "file":
			if haveFile {
				h.fail(w, r, httpx.BadRequest("upload one file at a time"))
				return
			}
			haveFile, name = true, part.FileName()
			if data, err = readPart(part); err != nil {
				h.fail(w, r, uploadError(err))
				return
			}
		}
		_ = part.Close()
	}
	if ws == uuid.Nil || !haveFile {
		h.fail(w, r, httpx.BadRequest("workspace_id and a file are required"))
		return
	}
	f, err := h.Files.Upload(r.Context(), user(r), ws, name, data)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, responses.NewFile(f))
}

func readPart(p *multipart.Part) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(p, files.MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > files.MaxFileSize {
		return nil, &http.MaxBytesError{Limit: files.MaxFileSize}
	}
	return b, nil
}

func uploadError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &httpx.Error{Status: http.StatusRequestEntityTooLarge, Code: httpx.CodeInvalidRequest, Message: "The file is larger than 20 MB"}
	}
	return httpx.BadRequest("the upload could not be read")
}

// ListFiles handles GET /api/v1/files?workspace_id=.
func (h *Handlers) ListFiles(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.queryID(w, r, "workspace_id")
	if !ok {
		return
	}
	fs, err := h.Files.List(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.List[responses.File]{Items: make([]responses.File, 0, len(fs))}
	for _, f := range fs {
		out.Items = append(out.Items, responses.NewFile(f))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// GetFile handles GET /api/v1/files/{fileID} (metadata).
func (h *Handlers) GetFile(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "fileID")
	if !ok {
		return
	}
	f, err := h.Files.Get(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewFile(f))
}

// DownloadFile handles GET /api/v1/files/{fileID}/content. The bytes are
// always sent as a download with a locked-down policy: an uploaded HTML or
// SVG file never runs as a page of this site.
func (h *Handlers) DownloadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "fileID")
	if !ok {
		return
	}
	f, data, err := h.Files.Download(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ct := f.ContentType
	if strings.HasPrefix(ct, "text/html") || strings.Contains(ct, "svg") || strings.Contains(ct, "xml") {
		ct = "application/octet-stream"
	}
	hd := w.Header()
	hd.Set("Content-Type", ct)
	hd.Set("Content-Length", strconv.Itoa(len(data)))
	hd.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(f.Name))
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// DeleteFile handles DELETE /api/v1/files/{fileID}.
func (h *Handlers) DeleteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "fileID")
	if !ok {
		return
	}
	if err := h.Files.Delete(r.Context(), user(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
