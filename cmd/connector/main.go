package main

import (
	"context"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"workflow-optimizer/internal/automation"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/infrastructure/encryption"
	"workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/integration/whatsapp"
	"workflow-optimizer/internal/orchestration"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	key, err := encryption.ParseKey(cfg.CredentialEncryptionKey)
	if err != nil {
		log.Fatal("connector encryption key is not configured")
	}
	encryptor, err := encryption.NewAESGCM(key)
	clear(key)
	if err != nil {
		log.Fatal(err)
	}
	manager := &whatsapp.Manager{Pool: store.Pool, DatabaseURL: cfg.DatabaseURL, Encryptor: encryptor, Context: ctx}
	tc, err := client.NewLazyClient(client.Options{HostPort: cfg.Automation.TemporalAddress, Namespace: cfg.Automation.TemporalNamespace})
	if err != nil {
		log.Fatal(err)
	}
	defer tc.Close()
	bus := &automation.EventBus{Store: postgres.NewAutomationRepository(store), Backend: &orchestration.Backend{Client: tc, Queue: cfg.Automation.TemporalTaskQueue}}
	manager.OnMessage = func(ctx context.Context, ws, user uuid.UUID, sender, id, text string) {
		event := automation.SourceEvent{ID: "whatsapp:" + ws.String() + ":" + user.String() + ":" + id, Kind: "whatsapp.message", WorkspaceID: ws, Source: user.String() + "/" + sender, Data: map[string]any{"sender": sender, "text": text, "account_user_id": user}}
		if err := bus.Emit(ctx, event); err != nil {
			log.Print("WhatsApp event could not be dispatched")
		}
	}
	defer manager.Close()
	addr := os.Getenv("CONNECTOR_ADDR")
	if addr == "" {
		addr = "127.0.0.1:58082"
	}
	server := &http.Server{Addr: addr, Handler: manager.Handler(cfg.Automation.ConnectorToken), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
