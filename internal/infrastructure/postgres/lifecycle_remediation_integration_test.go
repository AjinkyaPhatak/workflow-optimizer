package postgres_test

// Phase 8 remediation tests: adversarial PostgreSQL behaviour (ambiguous
// commits, lock contention, cancellation, raw SQL writers, forged history,
// migrations, precision, version immutability, partial failures).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/execution"
	postgresinfra "workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// runnerWith builds a Runner over store with extra test nodes and options.
func runnerWith(t *testing.T, store *postgresinfra.Store, opts execution.PersistenceOptions, extra ...node.Node) *execution.Runner {
	t.Helper()
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range extra {
		if err := a.NodeRegistry.RegisterNode(n, passDefinition(n.Type())); err != nil {
			t.Fatal(err)
		}
	}
	r, err := execution.NewRunner(execution.RunnerConfig{
		Executions:     postgresinfra.NewExecutionRepository(store),
		NodeExecutions: postgresinfra.NewNodeExecutionRepository(store),
		Definitions:    postgresinfra.NewWorkflowVersionRepository(store),
		Validator:      workflow.NewValidator(a.NodeRegistry),
		Graph:          execution.NewGraphExecutor(a.NodeRegistry),
		Persistence:    opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func middle(typ string) workflow.Definition {
	return wfDef(workflow.Node{ID: "mid", Type: typ, Name: "mid", Position: &workflow.Position{}, Config: map[string]any{}})
}

func (p *pgEnv) nodesOf(t *testing.T, id uuid.UUID) map[string]execution.NodeExecution {
	t.Helper()
	list, err := p.nodes.ListByExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]execution.NodeExecution{}
	for _, n := range list {
		out[n.NodeID] = n
	}
	return out
}

func (p *pgEnv) status(t *testing.T, id uuid.UUID) execution.ExecutionStatus {
	t.Helper()
	return p.get(t, id).Status
}

// holdRowLock locks the execution row from another connection until release.
func (p *pgEnv) holdRowLock(t *testing.T, id uuid.UUID) (release func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := openStore(t, p.url).Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM executions WHERE id = $1 FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() { once.Do(func() { _ = tx.Rollback(ctx) }) }
	t.Cleanup(release) // a failing test must not leave the lock (and pool) held
	return release
}

// gateNode blocks until released (or its context ends), then passes input on.
type gateNode struct {
	typ     string
	entered chan struct{}
	release chan struct{}
}

func newGate(typ string) *gateNode {
	return &gateNode{typ: typ, entered: make(chan struct{}, 1), release: make(chan struct{})}
}
func (g *gateNode) Type() string { return g.typ }
func (g *gateNode) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return node.NodeOutput{}, ctx.Err()
	}
	out := node.NewNodeOutput(nil)
	v, _ := in.GetPort("input")
	out.SetPort("output", v)
	return out, nil
}

// stubbornNode ignores cancellation and fails with its own error.
type stubbornNode struct{ entered chan struct{} }

func (stubbornNode) Type() string { return "test.stubborn" }
func (s stubbornNode) Execute(ctx context.Context, _ node.NodeInput) (node.NodeOutput, error) {
	s.entered <- struct{}{}
	<-ctx.Done()
	return node.NodeOutput{}, errors.New("upstream API said no")
}

// captureNode records the exact runtime input it receives.
type captureNode struct {
	mu  sync.Mutex
	got any
}

func (*captureNode) Type() string { return "test.capture" }
func (c *captureNode) Execute(_ context.Context, in node.NodeInput) (node.NodeOutput, error) {
	v, _ := in.GetPort("input")
	c.mu.Lock()
	c.got = v.Data
	c.mu.Unlock()
	out := node.NewNodeOutput(nil)
	out.SetPort("output", v)
	return out, nil
}

// ---------------------------------------------------------------------------
// Chaos proxy: a TCP relay that can drop the server's replies or kill the
// connection right after a chosen client message, creating genuinely
// ambiguous commits.
// ---------------------------------------------------------------------------

type chaosProxy struct {
	ln      net.Listener
	target  string
	pattern atomic.Value // []byte; armed when non-empty
	action  atomic.Value // "drop" | "kill"
	fired   atomic.Bool
}

func newChaosProxy(t *testing.T, target string) *chaosProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &chaosProxy{ln: ln, target: target}
	p.pattern.Store([]byte(nil))
	t.Cleanup(func() { _ = ln.Close() })
	go p.serve()
	return p
}

// arm makes the next client message containing pattern trigger action.
func (p *chaosProxy) arm(pattern, action string) {
	p.fired.Store(false)
	p.action.Store(action)
	p.pattern.Store([]byte(pattern))
}

func (p *chaosProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		server, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = client.Close()
			continue
		}
		var drop atomic.Bool
		go func() { // client -> server
			buf := make([]byte, 64<<10)
			for {
				n, err := client.Read(buf)
				if n > 0 {
					pat, _ := p.pattern.Load().([]byte)
					hit := len(pat) > 0 && bytes.Contains(buf[:n], pat) && p.fired.CompareAndSwap(false, true)
					if hit && p.action.Load() == "drop" {
						drop.Store(true) // this message's reply, and all later ones, are lost
					}
					if _, werr := server.Write(buf[:n]); werr != nil {
						return
					}
					if hit && p.action.Load() == "kill" {
						time.Sleep(150 * time.Millisecond)
						_ = client.Close()
						_ = server.Close()
						return
					}
				}
				if err != nil {
					_ = server.Close()
					return
				}
			}
		}()
		go func() { // server -> client
			buf := make([]byte, 64<<10)
			for {
				n, err := server.Read(buf)
				if n > 0 && !drop.Load() {
					if _, werr := client.Write(buf[:n]); werr != nil {
						return
					}
				}
				if err != nil {
					_ = client.Close()
					return
				}
			}
		}()
	}
}

// proxiedStore opens a pool through the chaos proxy. Every statement is sent
// with its SQL text (exec mode) so the proxy can recognise it.
func proxiedStore(t *testing.T, p *pgEnv) (*postgresinfra.Store, *chaosProxy) {
	t.Helper()
	u, err := url.Parse(p.url)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newChaosProxy(t, u.Host)
	u.Host = proxy.ln.Addr().String()
	q := u.Query()
	q.Set("default_query_exec_mode", "exec")
	u.RawQuery = q.Encode()
	return openStore(t, u.String()), proxy
}

// ---------------------------------------------------------------------------
// F1 — claims are never ambiguous
// ---------------------------------------------------------------------------

// The audit's orphan scenario: the claim is blocked on a row lock while the
// caller's context expires. Previously ~30% of runs left RUNNING executions
// nobody owned. Invariant now: when Run returns, the execution is never
// RUNNING, and a reported claim failure means it is still PENDING.
func TestPGClaimBlockedWhileCallerContextEnds(t *testing.T) {
	p := newPGEnv(t)
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			r := runnerWith(t, p.store, execution.PersistenceOptions{WriteTimeout: 3 * time.Second})
			for i := 0; i < 8; i++ {
				wf, v := p.seedWorkflow(t, wfDef(transformNode("x")))
				e, _ := r.Service().Create(context.Background(), wf, v, map[string]any{})
				release := p.holdRowLock(t, e.ID)
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				if mode == "cancel" {
					ctx, cancel = context.WithCancel(context.Background())
					go func() { time.Sleep(100 * time.Millisecond); cancel() }()
				}
				done := make(chan error, 1)
				go func() { _, err := r.Run(ctx, e.ID); done <- err }()
				time.Sleep(300 * time.Millisecond) // caller context is over; claim still queued on the lock
				release()
				runErr := <-done
				cancel()
				got := p.get(t, e.ID)
				if got.Status == execution.StatusRunning {
					t.Fatalf("round %d: execution left RUNNING after Run returned (err=%v)", i, runErr)
				}
				if errors.Is(runErr, execution.ErrExecutionNotClaimable) || strings.Contains(errString(runErr), "claim not attempted") {
					if got.Status != execution.StatusPending {
						t.Fatalf("round %d: claim reported failed but status is %s", i, got.Status)
					}
				}
				// The claim completed on its own bounded context, so the run
				// was finalized from the caller's context state.
				want := map[string]execution.ExecutionStatus{"deadline": execution.StatusFailed, "cancel": execution.StatusCancelled}[mode]
				if got.Status != want || got.ClaimToken == nil {
					t.Fatalf("round %d: status=%s token=%v err=%v", i, got.Status, got.ClaimToken, runErr)
				}
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// When the lock outlives the write budget, PostgreSQL gives up first and says
// so; the claim is definitely not applied and never materialises later.
func TestPGClaimLockTimeoutIsDefinite(t *testing.T) {
	p := newPGEnv(t)
	svc := execution.NewLifecycleServiceWithOptions(postgresinfra.NewExecutionRepository(p.store),
		execution.PersistenceOptions{WriteTimeout: 400 * time.Millisecond})
	e := p.create(t, nil)
	release := p.holdRowLock(t, e.ID)
	start := time.Now()
	err := svc.Start(context.Background(), e.ID)
	elapsed := time.Since(start)
	if !errors.Is(err, execution.ErrPersistenceTimeout) {
		t.Fatalf("err = %v, want ErrPersistenceTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("claim waited %s; must be bounded by the write timeout", elapsed)
	}
	release()
	time.Sleep(500 * time.Millisecond)
	if got := p.get(t, e.ID); got.Status != execution.StatusPending || got.ClaimToken != nil {
		t.Fatalf("timed-out claim materialised later: %+v", got)
	}
	if h := historyPairs(p.history(t, e.ID)); !sameStrings(h, []string{"NULL->PENDING"}) {
		t.Fatalf("history = %v", h)
	}
}

// The COMMIT reached PostgreSQL but its reply was lost: the client cannot know
// the outcome. The writer fences the backend and reads the commit marker, so
// the caller is told the truth (claimed) instead of a false failure.
func TestPGClaimCommitReplyLostIsResolvedAsClaimed(t *testing.T) {
	p := newPGEnv(t)
	store, proxy := proxiedStore(t, p)
	svc := execution.NewLifecycleServiceWithOptions(postgresinfra.NewExecutionRepository(store),
		execution.PersistenceOptions{WriteTimeout: time.Second})
	e := p.create(t, nil)
	// Match the COMMIT query message itself ("commit\x00"); a bare "commit"
	// would also match BEGIN's "read committed".
	proxy.arm("commit\x00", "drop")
	if err := svc.Start(context.Background(), e.ID); err != nil {
		t.Fatalf("claim committed but Start reported: %v", err)
	}
	if !proxy.fired.Load() {
		t.Fatal("proxy never saw the COMMIT; the test did not create ambiguity")
	}
	got := p.get(t, e.ID)
	if got.Status != execution.StatusRunning || got.ClaimToken == nil {
		t.Fatalf("execution = %+v", got)
	}
	if h := historyPairs(p.history(t, e.ID)); !sameStrings(h, []string{"NULL->PENDING", "PENDING->RUNNING"}) {
		t.Fatalf("history = %v", h)
	}
}

// The connection dies while the claim waits on a lock. The writer fences the
// backend, so the claim can never commit afterwards: definitely not applied.
func TestPGClaimConnectionLostWhileBlockedIsNotApplied(t *testing.T) {
	p := newPGEnv(t)
	store, proxy := proxiedStore(t, p)
	svc := execution.NewLifecycleServiceWithOptions(postgresinfra.NewExecutionRepository(store),
		execution.PersistenceOptions{WriteTimeout: 3 * time.Second})
	e := p.create(t, nil)
	release := p.holdRowLock(t, e.ID)
	proxy.arm("UPDATE executions SET", "kill")
	err := svc.Start(context.Background(), e.ID)
	release()
	if !errors.Is(err, execution.ErrWriteNotApplied) {
		t.Fatalf("err = %v, want ErrWriteNotApplied", err)
	}
	time.Sleep(500 * time.Millisecond)
	if got := p.get(t, e.ID); got.Status != execution.StatusPending {
		t.Fatalf("claim reported not applied but status is %s", got.Status)
	}
}

// F12: whatever the database's default isolation, the claim loser gets the
// lifecycle error, not a raw serialization failure.
func TestPGClaimUnderRepeatableReadDefault(t *testing.T) {
	p := newPGEnv(t)
	u, _ := url.Parse(p.url)
	q := u.Query()
	q.Set("default_transaction_isolation", "repeatable read")
	u.RawQuery = q.Encode()
	svcs := []*execution.LifecycleService{
		execution.NewLifecycleService(postgresinfra.NewExecutionRepository(openStore(t, u.String()))),
		execution.NewLifecycleService(postgresinfra.NewExecutionRepository(openStore(t, u.String()))),
	}
	for i := 0; i < 25; i++ {
		e := p.create(t, nil)
		start := make(chan struct{})
		res := make(chan error, 2)
		for _, s := range svcs {
			go func(s *execution.LifecycleService) { <-start; res <- s.Start(context.Background(), e.ID) }(s)
		}
		close(start)
		wins := 0
		for j := 0; j < 2; j++ {
			err := <-res
			switch {
			case err == nil:
				wins++
			case !errors.Is(err, execution.ErrExecutionNotClaimable):
				t.Fatalf("round %d: loser error = %v", i, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: wins = %d", i, wins)
		}
	}
}

// ---------------------------------------------------------------------------
// Terminal races
// ---------------------------------------------------------------------------

func TestPGTerminalRaces(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	other := execution.NewLifecycleService(postgresinfra.NewExecutionRepository(openStore(t, p.url)))
	ops := map[string]func(*execution.LifecycleService, uuid.UUID) error{
		"complete": func(s *execution.LifecycleService, id uuid.UUID) error {
			return s.Complete(ctx, id, map[string]any{"a": 1})
		},
		"fail": func(s *execution.LifecycleService, id uuid.UUID) error {
			return s.Fail(ctx, id, execution.ExecutionError{Code: "X", Message: "m"})
		},
		"cancel": func(s *execution.LifecycleService, id uuid.UUID) error { return s.Cancel(ctx, id) },
	}
	for _, pair := range [][2]string{{"complete", "fail"}, {"complete", "cancel"}, {"fail", "cancel"}} {
		t.Run(pair[0]+"_vs_"+pair[1], func(t *testing.T) {
			for i := 0; i < 25; i++ {
				e := p.create(t, nil)
				_ = p.svc.Start(ctx, e.ID)
				start := make(chan struct{})
				ra, rb := make(chan error, 1), make(chan error, 1)
				go func() { <-start; ra <- ops[pair[0]](p.svc, e.ID) }()
				go func() { <-start; rb <- ops[pair[1]](other, e.ID) }()
				close(start)
				ea, eb := <-ra, <-rb
				if (ea == nil) == (eb == nil) {
					t.Fatalf("round %d: exactly one terminal transition must win: %v / %v", i, ea, eb)
				}
				loser := ea
				if loser == nil {
					loser = eb
				}
				var te *execution.TransitionError
				if !errors.As(loser, &te) || !te.From.IsTerminal() {
					t.Fatalf("round %d: loser error = %v", i, loser)
				}
				terminal := 0
				for _, h := range p.history(t, e.ID) {
					if h.To.IsTerminal() {
						terminal++
					}
				}
				if terminal != 1 {
					t.Fatalf("round %d: %d terminal history records", i, terminal)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// F2 — node records only while the execution is RUNNING
// ---------------------------------------------------------------------------

func TestPGNodeWritesRequireRunningParent(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	machine := execution.NewNodeExecutionStateMachine(p.nodes)
	newRec := func(execID uuid.UUID, nodeID string) execution.NodeExecution {
		return execution.NodeExecution{ID: uuid.New(), ExecutionID: execID, NodeID: nodeID, NodeType: "text", Status: execution.NodeStatusPending}
	}

	pending := p.create(t, nil)
	if err := p.nodes.Create(ctx, newRec(pending.ID, "a")); !errors.Is(err, execution.ErrExecutionNotRunning) {
		t.Fatalf("PENDING parent Create: %v", err)
	}
	assertRejected(t, p.raw, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status) VALUES ($1, $2, 'raw', 'text', 'PENDING')", uuid.New(), pending.ID)

	finalize := map[execution.ExecutionStatus]func(uuid.UUID) error{
		execution.StatusCompleted: func(id uuid.UUID) error { return p.svc.Complete(ctx, id, nil) },
		execution.StatusFailed: func(id uuid.UUID) error {
			return p.svc.Fail(ctx, id, execution.ExecutionError{Code: "X", Message: "m"})
		},
		execution.StatusCancelled: func(id uuid.UUID) error { return p.svc.Cancel(ctx, id) },
	}
	for status, apply := range finalize {
		t.Run(string(status), func(t *testing.T) {
			e := p.create(t, nil)
			_ = p.svc.Start(ctx, e.ID)
			done := newRec(e.ID, "done")
			if err := machine.Begin(ctx, done, nil); err != nil {
				t.Fatal(err)
			}
			if err := machine.Transition(ctx, done.ID, execution.NodeStatusCompleted, execution.NodeTransitionUpdate{Output: map[string]any{}}); err != nil {
				t.Fatal(err)
			}
			pendingRec := newRec(e.ID, "pending")
			if err := p.nodes.Create(ctx, pendingRec); err != nil {
				t.Fatal(err)
			}
			if err := apply(e.ID); err != nil {
				t.Fatal(err)
			}
			if err := p.nodes.Create(ctx, newRec(e.ID, "late")); !errors.Is(err, execution.ErrExecutionNotRunning) {
				t.Fatalf("Create under %s: %v", status, err)
			}
			if err := machine.Begin(ctx, newRec(e.ID, "late2"), nil); !errors.Is(err, execution.ErrExecutionNotRunning) {
				t.Fatalf("Begin under %s: %v", status, err)
			}
			if err := machine.Transition(ctx, pendingRec.ID, execution.NodeStatusRunning, execution.NodeTransitionUpdate{}); !errors.Is(err, execution.ErrExecutionNotRunning) {
				t.Fatalf("Transition under %s: %v", status, err)
			}
			assertRejected(t, p.raw, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status) VALUES ($1, $2, 'raw', 'text', 'PENDING')", uuid.New(), e.ID)
			assertRejected(t, p.raw, "UPDATE node_executions SET status = 'RUNNING' WHERE id = $1", pendingRec.ID)
		})
	}
}

func TestPGCompleteRejectedWhileNodeRunning(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	e := p.create(t, nil)
	_ = p.svc.Start(ctx, e.ID)
	rec := execution.NodeExecution{ID: uuid.New(), ExecutionID: e.ID, NodeID: "busy", NodeType: "text", Status: execution.NodeStatusPending}
	if err := execution.NewNodeExecutionStateMachine(p.nodes).Begin(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.svc.Complete(ctx, e.ID, nil); !errors.Is(err, execution.ErrExecutionHasRunningNodes) {
		t.Fatalf("err = %v", err)
	}
	if p.status(t, e.ID) != execution.StatusRunning {
		t.Fatal("rejected completion changed status")
	}
	// Cancelling sweeps the RUNNING node in the same transaction.
	if err := p.svc.Cancel(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	busy := p.nodesOf(t, e.ID)["busy"]
	if busy.Status != execution.NodeStatusFailed || busy.Error.Code != execution.CodeCancelled || *busy.Error.NodeID != "busy" || busy.FinishedAt == nil {
		t.Fatalf("swept node = %+v / %+v", busy, busy.Error)
	}
}

// Cancel racing a node's completion write: the parent lock serializes them.
// Whatever wins, no node is left RUNNING and no node write lands after the
// execution became terminal.
func TestPGCancelRacingNodeWrites(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	other := execution.NewLifecycleService(postgresinfra.NewExecutionRepository(openStore(t, p.url)))
	machine := execution.NewNodeExecutionStateMachine(p.nodes)
	for i := 0; i < 30; i++ {
		e := p.create(t, nil)
		_ = p.svc.Start(ctx, e.ID)
		rec := execution.NodeExecution{ID: uuid.New(), ExecutionID: e.ID, NodeID: "n", NodeType: "text", Status: execution.NodeStatusPending}
		if err := machine.Begin(ctx, rec, nil); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		rn, rc := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			rn <- machine.Transition(ctx, rec.ID, execution.NodeStatusCompleted, execution.NodeTransitionUpdate{Output: map[string]any{}})
		}()
		go func() { <-start; rc <- other.Cancel(ctx, e.ID) }()
		close(start)
		nodeErr, cancelErr := <-rn, <-rc
		if cancelErr != nil {
			t.Fatalf("round %d: cancel failed: %v", i, cancelErr)
		}
		got := p.get(t, e.ID)
		n := p.nodesOf(t, e.ID)["n"]
		switch {
		case nodeErr == nil && n.Status == execution.NodeStatusCompleted && !n.FinishedAt.After(*got.FinishedAt):
		case errors.Is(nodeErr, execution.ErrExecutionNotRunning) && n.Status == execution.NodeStatusFailed:
		default:
			t.Fatalf("round %d: node=%s nodeErr=%v exec=%s", i, n.Status, nodeErr, got.Status)
		}
	}
}

// End-to-end: an operator cancels while a node runs in the owning worker.
func TestPGExternalCancellationStopsTheWorker(t *testing.T) {
	p := newPGEnv(t)
	gate := newGate("test.gate")
	r := runnerWith(t, p.store, execution.PersistenceOptions{}, gate)
	wf, v := p.seedWorkflow(t, middle("test.gate"))
	e, _ := r.Service().Create(context.Background(), wf, v, map[string]any{})
	res := make(chan error, 1)
	go func() { _, err := r.Run(context.Background(), e.ID); res <- err }()
	<-gate.entered
	if err := p.svc.Cancel(context.Background(), e.ID); err != nil { // another process
		t.Fatal(err)
	}
	cancelledAt := *p.get(t, e.ID).FinishedAt
	close(gate.release)
	err := <-res
	if !errors.Is(err, execution.ErrExecutionCancelled) {
		t.Fatalf("Run err = %v, want ErrExecutionCancelled", err)
	}
	recs := p.nodesOf(t, e.ID)
	for id, n := range recs {
		if n.CreatedAt.After(cancelledAt) || n.UpdatedAt.After(cancelledAt) {
			t.Fatalf("node %s written after cancellation: %+v", id, n)
		}
		if n.Status == execution.NodeStatusRunning {
			t.Fatalf("node %s left RUNNING", id)
		}
	}
	if mid := recs["mid"]; mid.Status != execution.NodeStatusFailed || mid.Error.Code != execution.CodeCancelled {
		t.Fatalf("mid = %+v", mid)
	}
	if _, ran := recs["shape"]; ran {
		t.Fatal("downstream node ran after cancellation")
	}
	if got := p.get(t, e.ID); got.Status != execution.StatusCancelled {
		t.Fatalf("status = %s", got.Status)
	}
}

// ---------------------------------------------------------------------------
// Cancellation and deadlines during node execution (F11, F13)
// ---------------------------------------------------------------------------

func TestPGCancellationDuringNode(t *testing.T) {
	p := newPGEnv(t)
	gate := newGate("test.gate")
	stub := stubbornNode{entered: make(chan struct{}, 1)}
	r := runnerWith(t, p.store, execution.PersistenceOptions{}, gate, stub)

	t.Run("node honours context", func(t *testing.T) {
		wf, v := p.seedWorkflow(t, middle("test.gate"))
		e, _ := r.Service().Create(context.Background(), wf, v, map[string]any{})
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-gate.entered; cancel() }()
		if _, err := r.Run(ctx, e.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		mid := p.nodesOf(t, e.ID)["mid"]
		if p.status(t, e.ID) != execution.StatusCancelled || mid.Status != execution.NodeStatusFailed || mid.Error.Code != execution.CodeCancelled {
			t.Fatalf("status=%s mid=%+v", p.status(t, e.ID), mid)
		}
	})
	t.Run("node ignores context", func(t *testing.T) {
		wf, v := p.seedWorkflow(t, middle("test.stubborn"))
		e, _ := r.Service().Create(context.Background(), wf, v, map[string]any{})
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-stub.entered; cancel() }()
		if _, err := r.Run(ctx, e.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		mid := p.nodesOf(t, e.ID)["mid"]
		if p.status(t, e.ID) != execution.StatusCancelled || mid.Status != execution.NodeStatusFailed || mid.Error.Code != execution.CodeNodeFailed {
			t.Fatalf("status=%s mid=%+v", p.status(t, e.ID), mid.Error)
		}
	})
	t.Run("deadline during node", func(t *testing.T) {
		wf, v := p.seedWorkflow(t, middle("test.gate"))
		e, _ := r.Service().Create(context.Background(), wf, v, map[string]any{})
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		go func() { <-gate.entered }()
		if _, err := r.Run(ctx, e.ID); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
		got := p.get(t, e.ID)
		mid := p.nodesOf(t, e.ID)["mid"]
		if got.Status != execution.StatusFailed || got.Error.Code != execution.CodeTimeout || mid.Error.Code != execution.CodeTimeout {
			t.Fatalf("exec=%+v mid=%+v", got.Error, mid.Error)
		}
	})
}

// F10: a finalization blocked by the database is bounded, never infinite.
func TestPGFinalizationIsBounded(t *testing.T) {
	p := newPGEnv(t)
	gate := newGate("test.gate")
	r := runnerWith(t, p.store, execution.PersistenceOptions{WriteTimeout: 500 * time.Millisecond}, gate)
	wf, v := p.seedWorkflow(t, middle("test.gate"))
	e, _ := r.Service().Create(context.Background(), wf, v, map[string]any{})
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { _, err := r.Run(ctx, e.ID); res <- err }()
	<-gate.entered
	release := p.holdRowLock(t, e.ID) // the database will not let anyone finalize
	cancel()
	select {
	case err := <-res:
		if !errors.Is(err, execution.ErrPersistenceTimeout) {
			t.Fatalf("err = %v, want ErrPersistenceTimeout", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("finalization hung after caller cancellation")
	}
	release()
	// Documented Phase 8 boundary: the database refused every write, so the
	// execution is still RUNNING, identifiable by its claim token.
	if got := p.get(t, e.ID); got.Status != execution.StatusRunning || got.ClaimToken == nil {
		t.Fatalf("execution = %+v", got)
	}
}

// ---------------------------------------------------------------------------
// F9 — node record atomicity and partial failure
// ---------------------------------------------------------------------------

func TestPGNodeFinishRecordingFailure(t *testing.T) {
	p := newPGEnv(t)
	r := runnerWith(t, p.store, execution.PersistenceOptions{})
	mustExec(t, p.raw, `CREATE FUNCTION test_block_mid_completion() RETURNS trigger AS $$ BEGIN
	  IF NEW.node_id = 'mid' AND NEW.status = 'COMPLETED' THEN RAISE EXCEPTION 'simulated outage'; END IF; RETURN NEW; END $$ LANGUAGE plpgsql`)
	mustExec(t, p.raw, `CREATE TRIGGER test_block_mid_completion BEFORE UPDATE ON node_executions FOR EACH ROW EXECUTE FUNCTION test_block_mid_completion()`)
	wf, v := p.seedWorkflow(t, wfDef(transformNode("x")))
	e, _ := r.Service().Create(context.Background(), wf, v, map[string]any{})
	if _, err := r.Run(context.Background(), e.ID); err == nil {
		t.Fatal("expected failure")
	}
	got := p.get(t, e.ID)
	mid := p.nodesOf(t, e.ID)["mid"]
	if got.Status != execution.StatusFailed || got.Error.Code != execution.CodeNodeObservation {
		t.Fatalf("execution = %+v", got.Error)
	}
	if mid.Status != execution.NodeStatusFailed || mid.Error.Code != execution.CodeNodeObservation ||
		!strings.Contains(mid.Error.Message, "executed") || mid.StartedAt == nil {
		t.Fatalf("mid = %+v / %+v", mid, mid.Error)
	}
	for id, n := range p.nodesOf(t, e.ID) {
		if n.Status == execution.NodeStatusRunning || n.Status == execution.NodeStatusPending {
			t.Fatalf("node %s left %s under a FAILED execution", id, n.Status)
		}
	}
}

// Begin is one transaction: if moving to RUNNING fails, no PENDING row stays.
func TestPGNodeBeginIsAtomic(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	mustExec(t, p.raw, `CREATE FUNCTION test_block_start() RETURNS trigger AS $$ BEGIN
	  IF NEW.node_id = 'x' AND NEW.status = 'RUNNING' THEN RAISE EXCEPTION 'simulated outage'; END IF; RETURN NEW; END $$ LANGUAGE plpgsql`)
	mustExec(t, p.raw, `CREATE TRIGGER test_block_start BEFORE UPDATE ON node_executions FOR EACH ROW EXECUTE FUNCTION test_block_start()`)
	e := p.create(t, nil)
	_ = p.svc.Start(ctx, e.ID)
	rec := execution.NodeExecution{ID: uuid.New(), ExecutionID: e.ID, NodeID: "x", NodeType: "text", Status: execution.NodeStatusPending}
	if err := execution.NewNodeExecutionStateMachine(p.nodes).Begin(ctx, rec, nil); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := p.nodes.Get(ctx, rec.ID); !errors.Is(err, execution.ErrNodeExecutionNotFound) {
		t.Fatalf("partial node record left behind: %v", err)
	}
}

// ---------------------------------------------------------------------------
// F5 / F6 — the database is the lifecycle authority
// ---------------------------------------------------------------------------

func TestPGRawSQLCannotBypassLifecycle(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	wf, v := p.seedWorkflow(t, nil)
	assertRejected(t, p.raw, "INSERT INTO executions (id, workflow_id, workflow_version_id, status, started_at) VALUES ($1,$2,$3,'RUNNING',now())", uuid.New(), wf, v)
	assertRejected(t, p.raw, "INSERT INTO executions (id, workflow_id, workflow_version_id, status, started_at, finished_at) VALUES ($1,$2,$3,'COMPLETED',now(),now())", uuid.New(), wf, v)
	assertRejected(t, p.raw, "INSERT INTO executions (id, workflow_id, workflow_version_id, status, claim_token) VALUES ($1,$2,$3,'PENDING',$4)", uuid.New(), wf, v, uuid.New())

	e := p.create(t, map[string]any{"a": 1})
	assertRejected(t, p.raw, "UPDATE executions SET status = 'RUNNING' WHERE id = $1", e.ID) // no claim token
	assertRejected(t, p.raw, "UPDATE executions SET updated_at = '2000-01-01' WHERE id = $1", e.ID)
	assertRejected(t, p.raw, "UPDATE executions SET input = '{}' WHERE id = $1", e.ID)
	assertRejected(t, p.raw, "DELETE FROM executions WHERE id = $1", e.ID)

	// A raw writer that follows the rules gets DB-generated timestamps and history.
	mustExec(t, p.raw, "UPDATE executions SET status = 'RUNNING', claim_token = $2, started_at = '1999-01-01', updated_at = '1999-01-01' WHERE id = $1", e.ID, uuid.New())
	got := p.get(t, e.ID)
	if got.StartedAt.Year() < 2020 || got.UpdatedAt.Year() < 2020 {
		t.Fatalf("caller-supplied timestamps were accepted: %+v", got)
	}
	if h := historyPairs(p.history(t, e.ID)); !sameStrings(h, []string{"NULL->PENDING", "PENDING->RUNNING"}) {
		t.Fatalf("history not generated from the status change: %v", h)
	}
	assertRejected(t, p.raw, "UPDATE executions SET error = '{\"code\":\"FORGED\"}' WHERE id = $1", e.ID)
	assertRejected(t, p.raw, "UPDATE executions SET claim_token = $2 WHERE id = $1", e.ID, uuid.New())
	assertRejected(t, p.raw, "UPDATE executions SET status = 'COMPLETED', error = '{\"code\":\"X\"}' WHERE id = $1", e.ID)
	assertRejected(t, p.raw, "UPDATE executions SET status = 'FAILED' WHERE id = $1", e.ID) // FAILED needs an error

	n := uuid.New()
	mustExec(t, p.raw, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status) VALUES ($1, $2, 'x', 'text', 'PENDING')", n, e.ID)
	assertRejected(t, p.raw, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status, started_at) VALUES ($1, $2, 'y', 'text', 'RUNNING', now())", uuid.New(), e.ID)
	mustExec(t, p.raw, "UPDATE node_executions SET status = 'RUNNING', input = '{\"a\":1}' WHERE id = $1", n)
	assertRejected(t, p.raw, "UPDATE node_executions SET input = '{\"forged\":true}' WHERE id = $1", n)
	assertRejected(t, p.raw, "UPDATE node_executions SET attempt = 7 WHERE id = $1", n)
	assertRejected(t, p.raw, "UPDATE node_executions SET status = 'COMPLETED', attempt = 7 WHERE id = $1", n)
	assertRejected(t, p.raw, "UPDATE node_executions SET status = 'COMPLETED', error = '{\"code\":\"X\"}' WHERE id = $1", n)
	if err := p.svc.Complete(ctx, e.ID, nil); !errors.Is(err, execution.ErrExecutionHasRunningNodes) {
		t.Fatalf("complete with running node: %v", err)
	}

	// Terminal rows cannot be moved anywhere by a raw writer, even with
	// otherwise well-formed values.
	for _, finish := range []func(uuid.UUID) error{
		func(id uuid.UUID) error { return p.svc.Complete(ctx, id, nil) },
		func(id uuid.UUID) error {
			return p.svc.Fail(ctx, id, execution.ExecutionError{Code: "X", Message: "m"})
		},
		func(id uuid.UUID) error { return p.svc.Cancel(ctx, id) },
	} {
		term := p.create(t, nil)
		_ = p.svc.Start(ctx, term.ID)
		if err := finish(term.ID); err != nil {
			t.Fatal(err)
		}
		before := p.get(t, term.ID)
		assertRejected(t, p.raw, "UPDATE executions SET status = 'RUNNING', claim_token = $2, output = NULL, error = NULL WHERE id = $1", term.ID, uuid.New())
		assertRejected(t, p.raw, "UPDATE executions SET status = 'COMPLETED', output = '{}', error = NULL WHERE id = $1", term.ID)
		assertRejected(t, p.raw, "UPDATE executions SET status = 'CANCELLED', output = NULL WHERE id = $1", term.ID)
		assertRejected(t, p.raw, "UPDATE executions SET status = 'PENDING', claim_token = NULL, output = NULL, error = NULL WHERE id = $1", term.ID)
		if after := p.get(t, term.ID); after.Status != before.Status || !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("terminal %s row changed: %+v", before.Status, after)
		}
	}
}

func TestPGHistoryCannotBeForged(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	e := p.create(t, nil)
	_ = p.svc.Start(ctx, e.ID)
	// Even rows that look legitimate are rejected: history is generated only
	// from actual status changes.
	assertRejected(t, p.raw, "INSERT INTO execution_status_history (execution_id, from_status, to_status) VALUES ($1, 'RUNNING', 'COMPLETED')", e.ID)
	assertRejected(t, p.raw, "INSERT INTO execution_status_history (execution_id, from_status, to_status) VALUES ($1, 'RUNNING', 'CANCELLED')", e.ID)
	done := p.create(t, nil)
	_ = p.svc.Start(ctx, done.ID)
	_ = p.svc.Complete(ctx, done.ID, nil)
	assertRejected(t, p.raw, "INSERT INTO execution_status_history (execution_id, from_status, to_status) VALUES ($1, 'RUNNING', 'CANCELLED')", done.ID)
	h := p.history(t, done.ID)
	assertRejected(t, p.raw, "UPDATE execution_status_history SET to_status = 'FAILED' WHERE id = $1", h[2].ID)
	assertRejected(t, p.raw, "DELETE FROM execution_status_history WHERE id = $1", h[0].ID)
	assertRejected(t, p.raw, "TRUNCATE execution_status_history")
	// Every execution's history path equals its actual status path.
	for _, id := range []uuid.UUID{e.ID, done.ID} {
		hist := p.history(t, id)
		if hist[len(hist)-1].To != p.status(t, id) {
			t.Fatalf("history %v does not end at status %s", historyPairs(hist), p.status(t, id))
		}
	}
	if _, err := p.execs.History(ctx, uuid.New()); !errors.Is(err, execution.ErrExecutionNotFound) {
		t.Fatalf("History(unknown) = %v, want ErrExecutionNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// F7 — JSON numeric precision
// ---------------------------------------------------------------------------

func TestPGLargeIntegerInputSurvivesEndToEnd(t *testing.T) {
	p := newPGEnv(t)
	capture := &captureNode{}
	r := runnerWith(t, p.store, execution.PersistenceOptions{}, capture)
	wf, v := p.seedWorkflow(t, middle("test.capture"))
	const big = "9007199254740993" // 2^53 + 1: not representable as float64
	var input map[string]any
	dec := json.NewDecoder(strings.NewReader(`{"account_id": ` + big + `, "nested": {"ids": [` + big + `, 1.5]}}`))
	dec.UseNumber()
	if err := dec.Decode(&input); err != nil {
		t.Fatal(err)
	}
	e, err := r.Service().Create(context.Background(), wf, v, input)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.get(t, e.ID).Input["account_id"]; got != json.Number(big) {
		t.Fatalf("Get input = %#v", got)
	}
	if _, err := r.Run(context.Background(), e.ID); err != nil {
		t.Fatal(err)
	}
	received, _ := capture.got.(map[string]any)
	if received["account_id"] != json.Number(big) {
		t.Fatalf("node received %#v", received["account_id"])
	}
	var stored string
	_ = p.raw.QueryRow(context.Background(), "SELECT output->'out'->'result'->>'account_id' FROM executions WHERE id = $1", e.ID).Scan(&stored)
	if stored != big {
		t.Fatalf("persisted output = %s", stored)
	}
	if b, _ := json.Marshal(p.get(t, e.ID).Output); !strings.Contains(string(b), big) {
		t.Fatalf("Get output = %s", b)
	}
}

// ---------------------------------------------------------------------------
// F8 — workflow versions
// ---------------------------------------------------------------------------

func TestPGWorkflowVersionImmutability(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	wf, published := p.seedWorkflow(t, wfDef(transformNode("x")))
	draft, archived := uuid.New(), uuid.New()
	mustExec(t, p.raw, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 2, '{}', 'DRAFT')", draft, wf)
	mustExec(t, p.raw, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 3, '{}', 'PUBLISHED')", archived, wf)
	mustExec(t, p.raw, "UPDATE workflow_versions SET status = 'ARCHIVED' WHERE id = $1", archived)

	assertRejected(t, p.raw, "UPDATE workflow_versions SET definition = '{\"mutated\":true}' WHERE id = $1", published)
	assertRejected(t, p.raw, "UPDATE workflow_versions SET definition = '{\"mutated\":true}' WHERE id = $1", archived)
	assertRejected(t, p.raw, "UPDATE workflow_versions SET version_number = 99 WHERE id = $1", published)
	assertRejected(t, p.raw, "UPDATE workflow_versions SET status = 'DRAFT' WHERE id = $1", published)
	assertRejected(t, p.raw, "DELETE FROM workflow_versions WHERE id = $1", archived)
	mustExec(t, p.raw, "UPDATE workflow_versions SET definition = '{\"edited\":true}' WHERE id = $1", draft) // drafts stay editable

	var publishedAt *time.Time
	mustExec(t, p.raw, "UPDATE workflow_versions SET status = 'PUBLISHED' WHERE id = $1", draft)
	_ = p.raw.QueryRow(ctx, "SELECT published_at FROM workflow_versions WHERE id = $1", draft).Scan(&publishedAt)
	if publishedAt == nil {
		t.Fatal("publishing must stamp published_at")
	}
	assertRejected(t, p.raw, "UPDATE workflow_versions SET definition = '{\"late\":true}' WHERE id = $1", draft)
}

func TestPGOnlyPublishedVersionsStartExecutions(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	wf, published := p.seedWorkflow(t, nil)
	draft, archived := uuid.New(), uuid.New()
	mustExec(t, p.raw, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 2, '{}', 'DRAFT')", draft, wf)
	mustExec(t, p.raw, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 3, '{}', 'PUBLISHED')", archived, wf)
	mustExec(t, p.raw, "UPDATE workflow_versions SET status = 'ARCHIVED' WHERE id = $1", archived)
	for name, v := range map[string]uuid.UUID{"draft": draft, "archived": archived} {
		if _, err := p.svc.Create(ctx, wf, v, nil); !errors.Is(err, execution.ErrWorkflowVersionNotExecutable) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, err := p.svc.Create(ctx, wf, published, nil); err != nil {
		t.Fatalf("published: %v", err)
	}
}

// ---------------------------------------------------------------------------
// F3 / F4 — migrations (golang-migrate, the project's migration mechanism)
// ---------------------------------------------------------------------------

func TestPGMigrationUpgradesLegacyData(t *testing.T) {
	m, pool := freshMigrator(t)
	if err := m.Steps(1); err != nil {
		t.Fatal(err)
	}
	wfA, vA := seedV1(t, pool, "PUBLISHED")
	wfB, vB := seedV1(t, pool, "DRAFT")
	_ = wfB
	ids := map[string]uuid.UUID{}
	insert := func(name, sql string, args ...any) {
		ids[name] = uuid.New()
		mustExec(t, pool, sql, append([]any{ids[name]}, args...)...)
	}
	// Every row below is valid under 000001 and violated the old 000002.
	insert("completed_no_ts", "INSERT INTO executions (id, workflow_id, workflow_version_id, status, output) VALUES ($1,$2,$3,'COMPLETED','{\"r\":1}')", wfA, vA)
	insert("running_no_start", "INSERT INTO executions (id, workflow_id, workflow_version_id, status) VALUES ($1,$2,$3,'RUNNING')", wfA, vA)
	insert("failed_no_error", "INSERT INTO executions (id, workflow_id, workflow_version_id, status, started_at, completed_at) VALUES ($1,$2,$3,'FAILED',now(),now())", wfA, vA)
	insert("mismatched_version", "INSERT INTO executions (id, workflow_id, workflow_version_id, status) VALUES ($1,$2,$3,'PENDING')", wfA, vB)
	insert("pending_with_junk", "INSERT INTO executions (id, workflow_id, workflow_version_id, status, started_at, output, error) VALUES ($1,$2,$3,'PENDING',now(),'{\"x\":1}','{\"e\":1}')", wfA, vA)
	insert("node_cancelled", "INSERT INTO node_executions (id, execution_id, node_id, node_type, status) VALUES ($1,$2,'n','text','CANCELLED')", ids["running_no_start"])
	insert("node_dup", "INSERT INTO node_executions (id, execution_id, node_id, node_type, status, started_at, completed_at) VALUES ($1,$2,'n','text','COMPLETED',now(),now())", ids["running_no_start"])

	// Pinned to 000002 (Phase 10 added 000003, tested separately).
	if err := m.Migrate(2); err != nil {
		t.Fatalf("upgrade with legacy data failed: %v", err)
	}
	if v, dirty, _ := m.Version(); v != 2 || dirty {
		t.Fatalf("version=%d dirty=%v", v, dirty)
	}
	exec := func(name string) (status string, wf uuid.UUID, started, finished *time.Time, output, execErr, legacy []byte) {
		_ = pool.QueryRow(context.Background(), "SELECT status, workflow_id, started_at, finished_at, output, error, legacy_values FROM executions WHERE id = $1", ids[name]).
			Scan(&status, &wf, &started, &finished, &output, &execErr, &legacy)
		return
	}
	if s, _, st, fin, out, _, legacy := exec("completed_no_ts"); s != "COMPLETED" || st == nil || fin == nil || string(out) == "" || legacy == nil {
		t.Fatalf("completed_no_ts = %s %v %v %s", s, st, fin, legacy)
	}
	if s, _, st, fin, _, _, _ := exec("running_no_start"); s != "RUNNING" || st == nil || fin != nil {
		t.Fatalf("running_no_start = %s %v %v", s, st, fin)
	}
	if s, _, _, _, _, e, _ := exec("failed_no_error"); s != "FAILED" || !strings.Contains(string(e), "LEGACY_UNKNOWN_FAILURE") {
		t.Fatalf("failed_no_error = %s %s", s, e)
	}
	if _, wf, _, _, _, _, legacy := exec("mismatched_version"); wf != wfB || !strings.Contains(string(legacy), wfA.String()) {
		t.Fatalf("mismatched_version workflow=%s legacy=%s", wf, legacy)
	}
	if s, _, st, _, out, e, legacy := exec("pending_with_junk"); s != "PENDING" || st != nil || out != nil || e != nil || !strings.Contains(string(legacy), `"x": 1`) {
		t.Fatalf("pending_with_junk = %s %v %s %s %s", s, st, out, e, legacy)
	}
	var nodeStatus, code, origStatus string
	_ = pool.QueryRow(context.Background(), "SELECT status, error->>'code', legacy_values->'original'->>'status' FROM node_executions WHERE id = $1", ids["node_cancelled"]).Scan(&nodeStatus, &code, &origStatus)
	if nodeStatus != "FAILED" || code != "EXECUTION_CANCELLED" || origStatus != "CANCELLED" {
		t.Fatalf("legacy CANCELLED node = %s %s %s", nodeStatus, code, origStatus)
	}
	var attempts []int
	rows, _ := pool.Query(context.Background(), "SELECT attempt FROM node_executions WHERE node_id = 'n' ORDER BY attempt")
	for rows.Next() {
		var a int
		_ = rows.Scan(&a)
		attempts = append(attempts, a)
	}
	rows.Close()
	if len(attempts) != 2 || attempts[0] == attempts[1] {
		t.Fatalf("duplicate attempts not renumbered: %v", attempts)
	}
	var total, mismatched int
	_ = pool.QueryRow(context.Background(), "SELECT count(*) FROM executions").Scan(&total)
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM executions e WHERE e.status <> (
		SELECT h.to_status FROM execution_status_history h WHERE h.execution_id = e.id
		ORDER BY CASE h.to_status WHEN 'PENDING' THEN 0 WHEN 'RUNNING' THEN 1 ELSE 2 END DESC LIMIT 1)`).Scan(&mismatched)
	if total != 5 || mismatched != 0 {
		t.Fatalf("rows=%d executions whose history disagrees with status=%d", total, mismatched)
	}
}

func TestPGMigrationDownAndUpPreservesPhase8Data(t *testing.T) {
	m, pool := freshMigrator(t)
	// The repositories need the latest schema; DOWN then goes all the way
	// back to 000001 (through 000003 and 000002 DOWN) and UP returns.
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	wf, v := seedV1(t, pool, "PUBLISHED")
	ctx := context.Background()
	store := &postgresinfra.Store{Pool: pool}
	svc := execution.NewLifecycleService(postgresinfra.NewExecutionRepository(store))
	nodes := postgresinfra.NewNodeExecutionRepository(store)
	machine := execution.NewNodeExecutionStateMachine(nodes)
	e, err := svc.Create(ctx, wf, v, map[string]any{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	_ = svc.Start(ctx, e.ID)
	skipped := execution.NodeExecution{ID: uuid.New(), ExecutionID: e.ID, NodeID: "s", NodeType: "text", Status: execution.NodeStatusPending}
	_ = machine.Begin(ctx, skipped, nil)
	if err := machine.Transition(ctx, skipped.ID, execution.NodeStatusSkipped, execution.NodeTransitionUpdate{}); err != nil {
		t.Fatal(err)
	}
	_ = svc.Complete(ctx, e.ID, map[string]any{"ok": true})
	before, _ := postgresinfra.NewExecutionRepository(store).Get(ctx, e.ID)

	if err := m.Migrate(1); err != nil {
		t.Fatalf("DOWN failed with Phase 8 data: %v", err)
	}
	if v, dirty, _ := m.Version(); v != 1 || dirty {
		t.Fatalf("after down: version=%d dirty=%v", v, dirty)
	}
	var nodeStatus string
	var archivedHistory, archivedExecs int
	_ = pool.QueryRow(ctx, "SELECT status FROM node_executions WHERE id = $1", skipped.ID).Scan(&nodeStatus)
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM phase8_rollback_status_history WHERE execution_id = $1", e.ID).Scan(&archivedHistory)
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM phase8_rollback_executions WHERE id = $1 AND claim_token IS NOT NULL", e.ID).Scan(&archivedExecs)
	if nodeStatus != "CANCELLED" || archivedHistory != 3 || archivedExecs != 1 {
		t.Fatalf("after down: node=%s archived history=%d archived execs=%d", nodeStatus, archivedHistory, archivedExecs)
	}

	if err := m.Up(); err != nil {
		t.Fatalf("UP after DOWN failed: %v", err)
	}
	after, err := postgresinfra.NewExecutionRepository(store).Get(ctx, e.ID)
	if err != nil || after.ClaimToken == nil || *after.ClaimToken != *before.ClaimToken || after.Status != execution.StatusCompleted {
		t.Fatalf("after re-up: %+v %v", after, err)
	}
	if n, _ := nodes.Get(ctx, skipped.ID); n.Status != execution.NodeStatusSkipped {
		t.Fatalf("SKIPPED not restored: %s", n.Status)
	}
	h, _ := postgresinfra.NewExecutionRepository(store).History(ctx, e.ID)
	if got := historyPairs(h); !sameStrings(got, []string{"NULL->PENDING", "PENDING->RUNNING", "RUNNING->COMPLETED"}) {
		t.Fatalf("history after re-up = %v", got)
	}
	var leftover int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename LIKE 'phase8_rollback_%'").Scan(&leftover)
	if leftover != 0 {
		t.Fatalf("%d archive tables left after a clean restore", leftover)
	}
}
