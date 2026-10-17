// Package gateway is the worker WebSocket server.
//
// Sessions receives every terminal, including duplicates. Idempotency is the
// Sessions implementation's job; the gateway does not drop a retry.
// env_vars values are secret-bearing and are never written to the log.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/polds/factory/internal/protocol"
)

const (
	wsPath        = "/api/v1/selfhosted/worker/ws"
	maxMessage    = 4 << 20
	writeWait     = 10 * time.Second
	readWait      = 90 * time.Second
	factoryPrefix = "factory"
	factoryTeam   = "factory"
)

// WorkerInfo is one live websocket. WorkerID is a routing label; ConnID is the process.
//
// docs/04's handshake is worker_id and the bearer token. It does not carry a
// version or a backend. Version is the User-Agent when the request has one.
// Backend is the backend query parameter when the worker sends one. Both stay
// empty otherwise. The gateway does not invent docker or a version string.
type WorkerInfo struct {
	WorkerID string
	ConnID   string
	LastPing time.Time
	InFlight int
	Version  string
	Backend  string
}

// Sessions is the control-plane side of a worker connection.
// Terminal is invoked for every TaskCompleted and TaskFailed, including retries.
type Sessions interface {
	WorkerConnected(ctx context.Context, w WorkerInfo) error
	WorkerSeen(ctx context.Context, workerID string, at time.Time)
	Claimed(ctx context.Context, m protocol.TaskClaimed) error
	Terminal(ctx context.Context, workerID string, m any) error
	Rejected(ctx context.Context, m protocol.TaskRejected) error
	WorkerGone(ctx context.Context, connID string)
}

// Gateway serves /api/v1/selfhosted/worker/ws and pushes assignments to live workers.
type Gateway struct {
	auth     func(bearer string) (team string, ok bool)
	sessions Sessions
	log      *slog.Logger
	upgrader websocket.Upgrader

	mu       sync.Mutex
	conns    map[string]*conn
	byWorker map[string][]*conn
}

// New returns a gateway. auth maps a bearer token to its team scope.
// A nil logger discards log lines.
func New(auth func(bearer string) (team string, ok bool), s Sessions, log *slog.Logger) *Gateway {
	if auth == nil || s == nil {
		panic("gateway: auth and sessions are required")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	g := &Gateway{
		auth:     auth,
		sessions: s,
		log:      log,
		conns:    map[string]*conn{},
		byWorker: map[string][]*conn{},
	}
	g.upgrader = websocket.Upgrader{
		ReadBufferSize:  1 << 14,
		WriteBufferSize: 1 << 14,
		CheckOrigin:     func(*http.Request) bool { return true },
	}
	return g
}

// ServeHTTP upgrades an authenticated worker websocket.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != wsPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		g.log.Info("rejected upgrade", "reason", "auth")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	team, ok := g.auth(token)
	if !ok {
		g.log.Info("rejected upgrade", "reason", "auth")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	workerID := r.URL.Query().Get("worker_id")
	if workerID == "" {
		http.Error(w, "worker_id required", http.StatusBadRequest)
		return
	}
	// Version and backend are optional. docs/04 sends neither.
	version := r.UserAgent()
	backend := r.URL.Query().Get("backend")
	// docs/04 reserves the factory prefix the way upstream reserves warp.
	// Only a key scoped to the factory team may claim one.
	if strings.HasPrefix(workerID, factoryPrefix) && team != factoryTeam {
		g.log.Info("rejected upgrade", "reason", "reserved-worker-id", "worker_id", workerID)
		http.Error(w, "worker_id prefix reserved", http.StatusForbidden)
		return
	}

	ws, err := g.upgrader.Upgrade(w, r, nil)
	if err != nil {
		g.log.Info("upgrade", "err", err)
		return
	}
	id, err := newConnID()
	if err != nil {
		g.log.Error("conn id", "err", err)
		_ = ws.Close()
		return
	}
	c := newConn(g, ws, id, workerID, version, backend, r.Context())
	g.register(c)
	go c.writeLoop()

	if err := g.sessions.WorkerConnected(context.WithoutCancel(r.Context()), c.snapshot()); err != nil {
		g.log.Error("worker connected", "conn_id", c.id, "worker_id", workerID, "err", err)
		c.signalClose()
		<-c.writerDone
		g.unregister(c)
		return
	}
	c.readLoop()
}

// Assign writes task_assignment to one live connection for workerID.
// Among connections it picks the least in-flight, then the oldest.
func (g *Gateway) Assign(ctx context.Context, workerID string, a protocol.TaskAssignment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := protocol.Marshal(protocol.TypeTaskAssignment, a)
	if err != nil {
		return fmt.Errorf("assign %s: %w", a.TaskID, err)
	}
	g.mu.Lock()
	c := g.pickLocked(workerID)
	if c == nil {
		g.mu.Unlock()
		return fmt.Errorf("assign %s: no live connection for worker %s", a.TaskID, workerID)
	}
	if a.TaskID != "" {
		c.tasks[a.TaskID] = struct{}{}
	}
	g.mu.Unlock()

	// Keys only. Values ride the socket and must not enter the log.
	g.log.Info("assign",
		"worker_id", workerID,
		"conn_id", c.id,
		"task_id", a.TaskID,
		"execution_id", a.ExecutionID,
		"env_keys", sortedKeys(a.EnvVars),
	)
	if err := c.write(ctx, raw); err != nil {
		// A caller that gives up after the frame is queued may still deliver it.
		if !queuedGiveUp(err) {
			g.finishTask(c, a.TaskID)
		}
		return fmt.Errorf("assign %s: %w", a.TaskID, err)
	}
	return nil
}

// Cancel writes task_cancellation to the connection that holds taskID.
func (g *Gateway) Cancel(ctx context.Context, workerID, taskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := protocol.Marshal(protocol.TypeTaskCancellation, protocol.TaskCancellation{TaskID: taskID})
	if err != nil {
		return fmt.Errorf("cancel %s: %w", taskID, err)
	}
	g.mu.Lock()
	c := g.connForTaskLocked(workerID, taskID)
	g.mu.Unlock()
	if c == nil {
		return fmt.Errorf("cancel %s: no live connection for worker %s", taskID, workerID)
	}
	g.log.Info("cancel", "worker_id", workerID, "conn_id", c.id, "task_id", taskID)
	if err := c.write(ctx, raw); err != nil {
		return fmt.Errorf("cancel %s: %w", taskID, err)
	}
	return nil
}

// Connected lists live connections. The slice is a snapshot sorted by ConnID.
func (g *Gateway) Connected() []WorkerInfo {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]WorkerInfo, 0, len(g.conns))
	for id := range g.conns {
		out = append(out, g.conns[id].info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConnID < out[j].ConnID })
	return out
}

func (g *Gateway) register(c *conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.conns[c.id] = c
	g.byWorker[c.workerID] = append(g.byWorker[c.workerID], c)
}

func (g *Gateway) unregister(c *conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.conns, c.id)
	list := g.byWorker[c.workerID]
	next := make([]*conn, 0, len(list))
	for i := range list {
		if list[i].id != c.id {
			next = append(next, list[i])
		}
	}
	if len(next) == 0 {
		delete(g.byWorker, c.workerID)
	} else {
		g.byWorker[c.workerID] = next
	}
}

func (g *Gateway) pickLocked(workerID string) *conn {
	list := g.byWorker[workerID]
	var best *conn
	for i := range list {
		c := list[i]
		if best == nil || prefer(c, best) {
			best = c
		}
	}
	return best
}

func prefer(a, b *conn) bool {
	if len(a.tasks) != len(b.tasks) {
		return len(a.tasks) < len(b.tasks)
	}
	if !a.connectedAt.Equal(b.connectedAt) {
		return a.connectedAt.Before(b.connectedAt)
	}
	return a.id < b.id
}

func (g *Gateway) connForTaskLocked(workerID, taskID string) *conn {
	list := g.byWorker[workerID]
	for i := range list {
		if _, ok := list[i].tasks[taskID]; ok {
			return list[i]
		}
	}
	return g.pickLocked(workerID)
}

func (g *Gateway) finishTask(c *conn, taskID string) {
	if c == nil || taskID == "" {
		return
	}
	g.mu.Lock()
	delete(c.tasks, taskID)
	g.mu.Unlock()
}

func (g *Gateway) handle(c *conn, raw []byte) {
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		g.log.Warn("decode envelope", "conn_id", c.id, "err", err)
		return
	}
	ctx := context.WithoutCancel(c.ctx)
	switch env.Type {
	case protocol.TypeTaskClaimed:
		var m protocol.TaskClaimed
		if err := json.Unmarshal(env.Data, &m); err != nil {
			g.log.Warn("decode message", "conn_id", c.id, "type", env.Type, "err", err)
			return
		}
		g.log.Info("task_claimed", "conn_id", c.id, "task_id", m.TaskID, "worker_id", m.WorkerID)
		if err := g.sessions.Claimed(ctx, m); err != nil {
			g.log.Error("claimed", "conn_id", c.id, "task_id", m.TaskID, "err", err)
		}
	case protocol.TypeTaskCompleted:
		var m protocol.TaskCompleted
		if err := json.Unmarshal(env.Data, &m); err != nil {
			g.log.Warn("decode message", "conn_id", c.id, "type", env.Type, "err", err)
			return
		}
		g.deliverTerminal(c, ctx, env.Type, m.TaskID, m.ExecutionID, m)
	case protocol.TypeTaskFailed:
		var m protocol.TaskFailed
		if err := json.Unmarshal(env.Data, &m); err != nil {
			g.log.Warn("decode message", "conn_id", c.id, "type", env.Type, "err", err)
			return
		}
		g.deliverTerminal(c, ctx, env.Type, m.TaskID, m.ExecutionID, m)
	case protocol.TypeTaskRejected:
		var m protocol.TaskRejected
		if err := json.Unmarshal(env.Data, &m); err != nil {
			g.log.Warn("decode message", "conn_id", c.id, "type", env.Type, "err", err)
			return
		}
		g.finishTask(c, m.TaskID)
		g.log.Info("task_rejected", "conn_id", c.id, "task_id", m.TaskID)
		if err := g.sessions.Rejected(ctx, m); err != nil {
			g.log.Error("rejected", "conn_id", c.id, "task_id", m.TaskID, "err", err)
		}
	default:
		// Unknown types are wire input, not a closed enum. The body is not logged.
		g.log.Info("ignore message", "conn_id", c.id, "type", safeToken(env.Type))
	}
}

func (g *Gateway) deliverTerminal(c *conn, ctx context.Context, typ, taskID, executionID string, msg any) {
	g.finishTask(c, taskID)
	g.log.Info("terminal", "type", typ, "conn_id", c.id, "task_id", taskID, "execution_id", executionID)
	if err := g.sessions.Terminal(ctx, c.workerID, msg); err != nil {
		g.log.Error("terminal", "type", typ, "conn_id", c.id, "task_id", taskID, "err", err)
	}
}

type writeReq struct {
	data  []byte
	reply chan error
}

type conn struct {
	g           *Gateway
	ws          *websocket.Conn
	id          string
	workerID    string
	version     string
	backend     string
	ctx         context.Context
	connectedAt time.Time
	lastPing    time.Time
	tasks       map[string]struct{}

	writes     chan writeReq
	pongs      chan []byte
	done       chan struct{}
	writerDone chan struct{}
	closeOnce  sync.Once
}

func newConn(g *Gateway, ws *websocket.Conn, id, workerID, version, backend string, ctx context.Context) *conn {
	c := &conn{
		g:           g,
		ws:          ws,
		id:          id,
		workerID:    workerID,
		version:     version,
		backend:     backend,
		ctx:         ctx,
		connectedAt: time.Now(),
		tasks:       map[string]struct{}{},
		writes:      make(chan writeReq, 8),
		pongs:       make(chan []byte, 4),
		done:        make(chan struct{}),
		writerDone:  make(chan struct{}),
	}
	ws.SetReadLimit(maxMessage)
	_ = ws.SetReadDeadline(time.Now().Add(readWait))
	ws.SetPingHandler(c.handlePing)
	return c
}

func (c *conn) snapshot() WorkerInfo {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	return c.info()
}

func (c *conn) info() WorkerInfo {
	return WorkerInfo{
		WorkerID: c.workerID,
		ConnID:   c.id,
		LastPing: c.lastPing,
		InFlight: len(c.tasks),
		Version:  c.version,
		Backend:  c.backend,
	}
}

func (c *conn) handlePing(appData string) error {
	now := time.Now()
	c.g.mu.Lock()
	c.lastPing = now
	c.g.mu.Unlock()
	_ = c.ws.SetReadDeadline(now.Add(readWait))
	c.g.sessions.WorkerSeen(context.WithoutCancel(c.ctx), c.workerID, now)
	select {
	case <-c.done:
		return nil
	case c.pongs <- []byte(appData):
		return nil
	}
}

// writeLoop is the only goroutine that writes this connection.
func (c *conn) writeLoop() {
	defer close(c.writerDone)
	for {
		select {
		case <-c.done:
			return
		case p := <-c.pongs:
			if err := c.writeControl(websocket.PongMessage, p); err != nil {
				c.signalClose()
				return
			}
		case req := <-c.writes:
			err := c.writeData(req.data)
			req.reply <- err
			if err != nil {
				c.signalClose()
				return
			}
		}
	}
}

func (c *conn) write(ctx context.Context, data []byte) error {
	reply := make(chan error, 1)
	req := writeReq{data: data, reply: reply}
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", errNotSent, ctx.Err())
	case <-c.done:
		return fmt.Errorf("%w: %w", errNotSent, errClosed)
	case c.writes <- req:
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", errQueued, ctx.Err())
	case <-c.done:
		return fmt.Errorf("%w: %w", errQueued, errClosed)
	}
}

func (c *conn) writeData(data []byte) error {
	if err := c.ws.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.ws.WriteMessage(websocket.TextMessage, data)
}

func (c *conn) writeControl(messageType int, data []byte) error {
	deadline := time.Now().Add(writeWait)
	if err := c.ws.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return c.ws.WriteControl(messageType, data, deadline)
}

func (c *conn) readLoop() {
	defer func() {
		c.signalClose()
		<-c.writerDone
		c.g.unregister(c)
		c.g.sessions.WorkerGone(context.WithoutCancel(c.ctx), c.id)
		c.g.log.Info("worker gone", "conn_id", c.id, "worker_id", c.workerID)
	}()
	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			if !benignReadErr(err) {
				c.g.log.Info("read", "conn_id", c.id, "err", err)
			}
			return
		}
		_ = c.ws.SetReadDeadline(time.Now().Add(readWait))
		c.g.handle(c, raw)
	}
}

func (c *conn) signalClose() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.ws.Close()
	})
}

var (
	errClosed  = errors.New("gateway: connection closed")
	errNotSent = errors.New("gateway: message not sent")
	errQueued  = errors.New("gateway: message queued")
)

// queuedGiveUp reports that the frame was already handed to the writer and the
// caller stopped waiting. The task stays in flight because the worker may still
// receive it.
func queuedGiveUp(err error) bool {
	return errors.Is(err, errQueued)
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	return token, true
}

func newConnID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "conn_" + hex.EncodeToString(b[:]), nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func safeToken(s string) string {
	if s == "" || len(s) > 64 {
		return "unrecognized"
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return "unrecognized"
		}
	}
	return s
}

func benignReadErr(err error) bool {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, errClosed) {
		return true
	}
	return websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseAbnormalClosure,
		websocket.CloseNoStatusReceived,
	)
}
