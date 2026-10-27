package whatsapp

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"modernc.org/sqlite"
	"workflow-optimizer/internal/credential"
)

// The WhatsApp SQLite database lives only in memory. Every committed write is
// synchronously serialized and encrypted before it is acknowledged to whatsmeow.
// This preserves the library's SQL store without persisting private keys in clear.
type snapshotConnector struct {
	pool            *pgxpool.Pool
	encryptor       credential.SecretEncryptor
	workspace, user uuid.UUID
}
type snapshotEnvelope struct {
	Workspace uuid.UUID
	User      uuid.UUID
	Data      []byte
}
type serializer interface {
	Serialize() ([]byte, error)
	Deserialize([]byte) error
}

func (s *snapshotConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (s *snapshotConnector) Connect(ctx context.Context) (driver.Conn, error) {
	base, err := s.Driver().Open(":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	var encrypted []byte
	err = s.pool.QueryRow(ctx, `SELECT encrypted_data FROM whatsapp_sessions WHERE workspace_id=$1 AND user_id=$2`, s.workspace, s.user).Scan(&encrypted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		base.Close()
		return nil, err
	}
	if len(encrypted) > 0 {
		raw, err := s.encryptor.Decrypt(ctx, encrypted)
		if err != nil {
			base.Close()
			return nil, errors.New("WhatsApp session could not be decrypted")
		}
		defer clear(raw)
		var envelope snapshotEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Workspace != s.workspace || envelope.User != s.user {
			base.Close()
			return nil, errors.New("WhatsApp session scope does not match")
		}
		defer clear(envelope.Data)
		if err := base.(serializer).Deserialize(envelope.Data); err != nil {
			base.Close()
			return nil, err
		}
	}
	return &snapshotConn{Conn: base, owner: s}, nil
}
func (s *snapshotConnector) DB() *sql.DB {
	db := sql.OpenDB(s)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db
}

type snapshotConn struct {
	driver.Conn
	owner *snapshotConnector
	inTx  bool
}

func (c *snapshotConn) persist(ctx context.Context) error {
	if c.inTx {
		return nil
	}
	data, err := c.Conn.(serializer).Serialize()
	if err != nil {
		return err
	}
	defer clear(data)
	raw, err := json.Marshal(snapshotEnvelope{c.owner.workspace, c.owner.user, data})
	if err != nil {
		return err
	}
	defer clear(raw)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	encrypted, err := c.owner.encryptor.Encrypt(ctx, raw)
	if err != nil {
		return err
	}
	_, err = c.owner.pool.Exec(ctx, `INSERT INTO whatsapp_sessions(workspace_id,user_id,encrypted_data) VALUES($1,$2,$3)
 ON CONFLICT(workspace_id,user_id) DO UPDATE SET encrypted_data=EXCLUDED.encrypted_data,updated_at=now()`, c.owner.workspace, c.owner.user, encrypted)
	return err
}
func mutates(query string) bool {
	query = strings.ToUpper(strings.TrimSpace(query))
	return !strings.HasPrefix(query, "SELECT") && !strings.HasPrefix(query, "PRAGMA") && !strings.HasPrefix(query, "EXPLAIN")
}
func (c *snapshotConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	executor, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	result, err := executor.ExecContext(ctx, q, args)
	if err == nil && mutates(q) {
		err = c.persist(ctx)
	}
	return result, err
}
func (c *snapshotConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	query, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := query.QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	if mutates(q) {
		return &snapshotRows{Rows: rows, flush: func() error { return c.persist(ctx) }}, nil
	}
	return rows, nil
}
func (c *snapshotConn) CheckNamedValue(v *driver.NamedValue) error {
	if check, ok := c.Conn.(driver.NamedValueChecker); ok {
		return check.CheckNamedValue(v)
	}
	return driver.ErrSkip
}
func (c *snapshotConn) Prepare(q string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(q)
	if err != nil {
		return nil, err
	}
	return &snapshotStmt{Stmt: stmt, conn: c, query: q}, nil
}
func (c *snapshotConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *snapshotConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var tx driver.Tx
	var err error
	if begin, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = begin.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin()
	}
	if err != nil {
		return nil, err
	}
	c.inTx = true
	return &snapshotTx{Tx: tx, conn: c, ctx: ctx}, nil
}

type snapshotTx struct {
	driver.Tx
	conn *snapshotConn
	ctx  context.Context
}

func (t *snapshotTx) Commit() error {
	err := t.Tx.Commit()
	t.conn.inTx = false
	if err == nil {
		err = t.conn.persist(t.ctx)
	}
	return err
}
func (t *snapshotTx) Rollback() error { err := t.Tx.Rollback(); t.conn.inTx = false; return err }

type snapshotRows struct {
	driver.Rows
	flush  func() error
	closed bool
}

func (r *snapshotRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.Rows.Close()
	if err == nil {
		err = r.flush()
	}
	return err
}

type snapshotStmt struct {
	driver.Stmt
	conn  *snapshotConn
	query string
}

func (s *snapshotStmt) Exec(args []driver.Value) (driver.Result, error) {
	result, err := s.Stmt.Exec(args)
	if err == nil && mutates(s.query) {
		err = s.conn.persist(context.Background())
	}
	return result, err
}
func (s *snapshotStmt) Query(args []driver.Value) (driver.Rows, error) {
	rows, err := s.Stmt.Query(args)
	if err != nil {
		return nil, err
	}
	if mutates(s.query) {
		return &snapshotRows{Rows: rows, flush: func() error { return s.conn.persist(context.Background()) }}, nil
	}
	return rows, nil
}
