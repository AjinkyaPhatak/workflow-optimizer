// Package httpx holds the API's HTTP plumbing: JSON encoding and decoding,
// the error mapping, and pagination parsing.
package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

// MaxBodyBytes bounds every request body.
const MaxBodyBytes = 2 << 20

// WriteJSON writes v with status. API responses are never cached.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":{"code":"INTERNAL_ERROR","message":"Internal server error","details":null}}`)
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// NoContent writes 204.
func NoContent(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// DecodeJSON decodes a single JSON object into dst. Unknown fields, trailing
// data, a non-JSON content type and bodies over MaxBodyBytes are rejected
// with a 400 *Error.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || mt != "application/json" {
			return BadRequest("Content-Type must be application/json")
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: CodeInvalidRequest, Message: "Request body is too large"}
		}
		return BadRequest("Request body could not be read")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return BadRequest("Request body is required")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var syntax *json.SyntaxError
		var typ *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntax):
			return BadRequest("Request body is malformed JSON")
		case errors.As(err, &typ) && typ.Field != "":
			return BadRequest("Field " + typ.Field + " has the wrong type")
		case errors.As(err, &typ):
			return BadRequest("Request body must be a JSON object")
		default:
			// Unknown fields and other decoder errors name the field only.
			return BadRequest("Request body is invalid: " + err.Error())
		}
	}
	if dec.More() {
		return BadRequest("Request body must contain a single JSON object")
	}
	return nil
}
