package whatsapp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"workflow-optimizer/internal/credential"
)

type account struct {
	client *whatsmeow.Client
	db     *sql.DB
	lease  *pgx.Conn
	mu     sync.Mutex
	status Status
}
type Manager struct {
	Pool        *pgxpool.Pool
	DatabaseURL string
	Encryptor   credential.SecretEncryptor
	Context     context.Context
	OnMessage   func(context.Context, uuid.UUID, uuid.UUID, string, string, string)
	mu          sync.Mutex
	accounts    map[string]*account
}

func (m *Manager) get(ctx context.Context, ws, user uuid.UUID) (*account, error) {
	if m.Encryptor == nil {
		return nil, errors.New("WhatsApp session encryption is not configured")
	}
	key := ws.String() + "/" + user.String()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.accounts == nil {
		m.accounts = map[string]*account{}
	}
	if a := m.accounts[key]; a != nil {
		return a, nil
	}
	lease, err := pgx.Connect(ctx, m.DatabaseURL)
	if err != nil {
		return nil, err
	}
	var owned bool
	if err := lease.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "whatsapp/"+key).Scan(&owned); err != nil || !owned {
		_ = lease.Close(ctx)
		return nil, errors.New("this account is connected to another connector")
	}
	db := (&snapshotConnector{pool: m.Pool, encryptor: m.Encryptor, workspace: ws, user: user}).DB()
	container := sqlstore.NewWithDB(db, "sqlite3", nil)
	if err := container.Upgrade(ctx); err != nil {
		db.Close()
		_ = lease.Close(ctx)
		return nil, err
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		db.Close()
		_ = lease.Close(ctx)
		return nil, err
	}
	client := whatsmeow.NewClient(device, nil)
	a := &account{client: client, db: db, lease: lease, status: Status{State: "disconnected"}}
	client.AddEventHandler(func(event any) {
		switch e := event.(type) {
		case *events.Connected:
			a.mu.Lock()
			a.status = Status{State: "connected"}
			a.mu.Unlock()
		case *events.LoggedOut:
			a.mu.Lock()
			a.status = Status{State: "needs_pairing"}
			a.mu.Unlock()
		case *events.Disconnected:
			a.mu.Lock()
			a.status = Status{State: "reconnecting"}
			a.mu.Unlock()
		case *events.Message:
			if !e.Info.IsFromMe && m.OnMessage != nil {
				text := e.Message.GetConversation()
				if text == "" {
					text = e.Message.GetExtendedTextMessage().GetText()
				}
				if text != "" {
					m.OnMessage(m.Context, ws, user, e.Info.Sender.String(), e.Info.ID, text)
				}
			}
		case *events.Receipt:
			ids := make([]string, len(e.MessageIDs))
			for i, id := range e.MessageIDs {
				ids[i] = id
			}
			status := "delivered"
			if string(e.Type) == "read" || string(e.Type) == "read-self" {
				status = "read"
			}
			_, _ = m.Pool.Exec(m.Context, `UPDATE automation_actions SET result=jsonb_set(result,'{outputs,receipt,status}',to_jsonb($4::text)),updated_at=now()
    WHERE provider_receipt=ANY($1) AND run_id IN(SELECT id FROM automation_runs WHERE workspace_id=$2 AND user_id=$3)`, ids, ws, user, status)
		}
	})
	m.accounts[key] = a
	return a, nil
}
func (m *Manager) Connect(ctx context.Context, ws, user uuid.UUID) (Status, error) {
	a, err := m.get(ctx, ws, user)
	if err != nil {
		return Status{}, err
	}
	a.mu.Lock()
	if a.client.IsConnected() {
		out := a.status
		a.mu.Unlock()
		return out, nil
	}
	if a.status.State == "connecting" || a.status.State == "scan_qr" {
		out := a.status
		a.mu.Unlock()
		return out, nil
	}
	a.status = Status{State: "connecting"}
	a.mu.Unlock()
	var qr <-chan whatsmeow.QRChannelItem
	if a.client.Store.ID == nil {
		qr, err = a.client.GetQRChannel(m.Context)
		if err != nil {
			return Status{}, err
		}
	}
	go func() {
		if err := a.client.Connect(); err != nil {
			a.mu.Lock()
			a.status = Status{State: "disconnected"}
			a.mu.Unlock()
			return
		}
	}()
	if qr != nil {
		go func() {
			for item := range qr {
				a.mu.Lock()
				switch item.Event {
				case "code":
					a.status = Status{State: "scan_qr", QR: item.Code}
				case "success":
					a.status = Status{State: "connected"}
				case "timeout":
					a.status = Status{State: "needs_pairing"}
				}
				a.mu.Unlock()
			}
		}()
	}
	return Status{State: "connecting"}, nil
}
func (m *Manager) Status(ctx context.Context, ws, user uuid.UUID) (Status, error) {
	key := ws.String() + "/" + user.String()
	m.mu.Lock()
	a := m.accounts[key]
	m.mu.Unlock()
	if a == nil {
		var exists bool
		if err := m.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM whatsapp_sessions WHERE workspace_id=$1 AND user_id=$2)`, ws, user).Scan(&exists); err != nil {
			return Status{}, err
		}
		if !exists {
			return Status{State: "disconnected"}, nil
		}
		return m.Connect(ctx, ws, user)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status, nil
}
func (m *Manager) Contacts(ctx context.Context, ws, user uuid.UUID) ([]Contact, error) {
	a, err := m.get(ctx, ws, user)
	if err != nil {
		return nil, err
	}
	if !a.client.IsConnected() {
		_, _ = m.Connect(ctx, ws, user)
		return nil, errors.New("WhatsApp is reconnecting")
	}
	contacts, err := a.client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return nil, err
	}
	out := []Contact{}
	for jid, info := range contacts {
		name := info.FullName
		if name == "" {
			name = info.PushName
		}
		if name == "" {
			name = info.BusinessName
		}
		if name == "" {
			name = jid.User
		}
		out = append(out, Contact{jid.String(), name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (m *Manager) Send(ctx context.Context, ws, user uuid.UUID, in SendRequest) (Receipt, error) {
	a, err := m.get(ctx, ws, user)
	if err != nil {
		return Receipt{}, err
	}
	if !a.client.IsConnected() {
		return Receipt{}, errors.New("WhatsApp is disconnected")
	}
	if len(in.Text) > 20000 || len(in.Data) > 20<<20 || in.OperationID == "" {
		return Receipt{}, errors.New("invalid message")
	}
	jid, err := types.ParseJID(in.Recipient)
	if err != nil || !strings.Contains(in.Recipient, "@") {
		return Receipt{}, errors.New("choose a contact ID")
	}
	// Caller must resolve the exact contact; reject invented or cross-account JIDs.
	known, err := a.client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return Receipt{}, err
	}
	if _, ok := known[jid]; !ok {
		return Receipt{}, errors.New("recipient is not in this account's contacts")
	}
	message := &waE2E.Message{Conversation: proto.String(in.Text)}
	if len(in.Data) > 0 {
		upload, err := a.client.Upload(ctx, in.Data, whatsmeow.MediaDocument)
		if err != nil {
			return Receipt{}, err
		}
		message = &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			URL: proto.String(upload.URL), DirectPath: proto.String(upload.DirectPath), MediaKey: upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256, FileSHA256: upload.FileSHA256, FileLength: proto.Uint64(uint64(len(in.Data))),
			FileName: proto.String(in.Filename), Mimetype: proto.String(in.ContentType), Caption: proto.String(in.Text),
		}}
	}
	hash := sha256.Sum256([]byte(in.OperationID))
	id := strings.ToUpper(hex.EncodeToString(hash[:16]))
	response, err := a.client.SendMessage(ctx, jid, message, whatsmeow.SendRequestExtra{ID: id})
	if err != nil {
		return Receipt{}, errors.New("message delivery is uncertain")
	}
	return Receipt{ID: response.ID, Status: "sent"}, nil
}
func (m *Manager) Resolve(ctx context.Context, ws, user uuid.UUID, query string) ([]Contact, error) {
	contacts, err := m.Contacts(ctx, ws, user)
	if err != nil {
		return nil, err
	}
	matches := []Contact{}
	for _, contact := range contacts {
		if contact.ID == query {
			return []Contact{contact}, nil
		}
		if strings.Contains(strings.ToLower(contact.Name), strings.ToLower(query)) {
			matches = append(matches, contact)
		}
	}
	if len(matches) > 0 {
		return matches, nil
	}
	phone := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(query)
	if !strings.HasPrefix(phone, "+") || len(phone) < 9 || len(phone) > 16 {
		return matches, nil
	}
	for _, digit := range phone[1:] {
		if digit < '0' || digit > '9' {
			return matches, nil
		}
	}
	a, err := m.get(ctx, ws, user)
	if err != nil {
		return nil, err
	}
	results, err := a.client.IsOnWhatsApp(ctx, []string{phone})
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		if result.IsIn {
			if err := a.client.Store.Contacts.PutContactName(ctx, result.JID, phone, ""); err != nil {
				return nil, err
			}
			matches = append(matches, Contact{ID: result.JID.String(), Name: phone})
		}
	}
	return matches, nil
}
func (m *Manager) Disconnect(ctx context.Context, ws, user uuid.UUID) error {
	key := ws.String() + "/" + user.String()
	m.mu.Lock()
	a := m.accounts[key]
	delete(m.accounts, key)
	m.mu.Unlock()
	if a != nil {
		if a.client.IsConnected() {
			_ = a.client.Logout(ctx)
		}
		a.client.Disconnect()
		_ = a.db.Close()
	}
	_, err := m.Pool.Exec(ctx, `DELETE FROM whatsapp_sessions WHERE workspace_id=$1 AND user_id=$2`, ws, user)
	if a != nil {
		_ = a.lease.Close(context.WithoutCancel(ctx))
	}
	return err
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.accounts {
		a.client.Disconnect()
		_ = a.db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.lease.Close(ctx)
		cancel()
	}
}
