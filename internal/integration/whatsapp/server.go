package whatsapp

import (
	"crypto/hmac"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
)

func (m *Manager) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/accounts/{workspace}/{user}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if token == "" || !hmac.Equal([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) {
			http.Error(w, "Unauthorized", 401)
			return
		}
		ws, err := uuid.Parse(r.PathValue("workspace"))
		if err != nil {
			http.Error(w, "Invalid workspace", 400)
			return
		}
		user, err := uuid.Parse(r.PathValue("user"))
		if err != nil {
			http.Error(w, "Invalid user", 400)
			return
		}
		var out any
		switch r.Method + " " + r.PathValue("action") {
		case "GET status":
			out, err = m.Status(r.Context(), ws, user)
		case "GET contacts":
			out, err = m.Contacts(r.Context(), ws, user)
		case "POST connect":
			out, err = m.Connect(r.Context(), ws, user)
		case "POST disconnect":
			err = m.Disconnect(r.Context(), ws, user)
		case "POST send":
			var in SendRequest
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 30<<20)).Decode(&in) != nil {
				http.Error(w, "Invalid message", 400)
				return
			}
			out, err = m.Send(r.Context(), ws, user, in)
		case "POST resolve":
			var in struct {
				Query string `json:"query"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4000)).Decode(&in) != nil {
				http.Error(w, "Invalid query", 400)
				return
			}
			out, err = m.Resolve(r.Context(), ws, user, in.Query)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "WhatsApp connection could not complete the request", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(out)
	})
	return mux
}
