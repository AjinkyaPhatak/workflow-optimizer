package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestContactsWaitsForRestoredConnection(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/contacts") {
			t.Error("expected read-only contact lookup")
		}
		if calls.Add(1) == 1 {
			http.Error(w, "reconnecting", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`[{"id":"person","name":"Ajinkya Phatak"}]`))
	}))
	defer server.Close()
	client := &Client{URL: server.URL, Token: "test"}
	contacts, err := client.Contacts(context.Background(), uuid.New(), uuid.New())
	if err != nil || len(contacts) != 1 || calls.Load() != 2 {
		t.Fatalf("contacts=%v error=%v calls=%d", contacts, err, calls.Load())
	}
}

func TestContactsDoesNotRetryAuthenticationFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "secret connector response", http.StatusUnauthorized)
	}))
	defer server.Close()
	client := &Client{URL: server.URL, Token: "test"}
	_, err := client.Contacts(context.Background(), uuid.New(), uuid.New())
	if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error=%v calls=%d", err, calls.Load())
	}
}

func TestContactsReconnectWaitCanBeCancelled(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := (&Client{URL: server.URL, Token: "test"}).Contacts(ctx, uuid.New(), uuid.New())
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("error=%v calls=%d", err, calls.Load())
	}
}

func TestSendIsNeverRetriedOnConnectionFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	_, err := (&Client{URL: server.URL, Token: "test"}).Send(context.Background(), uuid.New(), uuid.New(), SendRequest{Recipient: "person", Text: "requested message", OperationID: "stable"})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("error=%v sends=%d", err, calls.Load())
	}
}
