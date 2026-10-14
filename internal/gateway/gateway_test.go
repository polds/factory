package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/polds/factory/internal/protocol"
)

func TestBadBearerRejectedBeforeUpgrade(t *testing.T) {
	g, fake, srv := startGateway(t, nil)
	_ = g

	status, upgrade, err := dialStatus(t, wsURL(srv, "w1"), "nope")
	if err == nil {
		t.Fatal("dial succeeded")
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if strings.Contains(strings.ToLower(upgrade), "websocket") || status == http.StatusSwitchingProtocols {
		t.Fatal("connection was upgraded")
	}
	if fake.connectedCount() != 0 {
		t.Fatalf("sessions saw a connection: %+v", fake.connected())
	}
}

func TestWorkerIDRequired(t *testing.T) {
	_, _, srv := startGateway(t, nil)
	raw := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/selfhosted/worker/ws"
	status, _, err := dialStatus(t, raw, "worker-key")
	if err == nil {
		t.Fatal("dial succeeded")
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d err=%v", status, err)
	}
}

func TestFactoryPrefixRefusedForNonFactoryKey(t *testing.T) {
	_, fake, srv := startGateway(t, nil)

	status, _, err := dialStatus(t, wsURL(srv, "factory-1"), "worker-key")
	if err == nil {
		t.Fatal("factory-prefixed id was accepted")
	}
	if status != http.StatusForbidden {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if status == http.StatusSwitchingProtocols {
		t.Fatal("upgraded")
	}
	if fake.connectedCount() != 0 {
		t.Fatal("reserved id was registered")
	}

	// The same key can still connect with an ordinary id, and a factory-team
	// key can use the reserved prefix.
	ordinary := dialClient(t, wsURL(srv, "acme-1"), "worker-key")
	fake.waitConnected(t, 1)
	_ = ordinary.conn.Close()

	reserved := dialClient(t, wsURL(srv, "factory-1"), "factory-key")
	fake.waitConnected(t, 2)
	_ = reserved
}

func TestPingPongCallsWorkerSeen(t *testing.T) {
	g, fake, srv := startGateway(t, nil)
	c := dialClient(t, wsURL(srv, "w1"), "worker-key")
	fake.waitConnected(t, 1)

	if err := c.conn.WriteControl(websocket.PingMessage, []byte("hb-1"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-c.pongs:
		if got != "hb-1" {
			t.Fatalf("pong payload %q", got)
		}
	case err := <-c.errc:
		t.Fatalf("read: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for pong")
	}

	seen := fake.seenCalls()
	if len(seen) == 0 || seen[len(seen)-1].workerID != "w1" || seen[len(seen)-1].at.IsZero() {
		t.Fatalf("WorkerSeen %+v", seen)
	}
	infos := g.Connected()
	if len(infos) != 1 || infos[0].WorkerID != "w1" || infos[0].LastPing.IsZero() {
		t.Fatalf("connected %+v", infos)
	}
}

func TestAssignDeliversTaskAssignment(t *testing.T) {
	g, fake, srv := startGateway(t, nil)
	c := dialClient(t, wsURL(srv, "w1"), "worker-key")
	fake.waitConnected(t, 1)

	want := protocol.TaskAssignment{
		TaskID:      "run_1",
		ExecutionID: "exec_1",
		Task:        &protocol.Task{ID: "run_1", Title: "hello", Definition: protocol.TaskDefinition{Prompt: "do it"}},
	}
	if err := g.Assign(context.Background(), "w1", want); err != nil {
		t.Fatal(err)
	}
	env := c.readOne(t)
	if env.Type != protocol.TypeTaskAssignment {
		t.Fatalf("type %s", env.Type)
	}
	var got protocol.TaskAssignment
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "run_1" || got.ExecutionID != "exec_1" || got.Task == nil || got.Task.Title != "hello" {
		t.Fatalf("%+v", got)
	}
	if err := g.Assign(context.Background(), "missing", want); err == nil {
		t.Fatal("assign to unknown worker")
	}
}

func TestDuplicateTerminalIsForwarded(t *testing.T) {
	g, fake, srv := startGateway(t, nil)
	c := dialClient(t, wsURL(srv, "w1"), "worker-key")
	fake.waitConnected(t, 1)

	claimed := protocol.TaskClaimed{TaskID: "run_1", WorkerID: "w1"}
	completed := protocol.TaskCompleted{TaskID: "run_1", ExecutionID: "exec_1", Message: "done"}
	writeMsg(t, c, protocol.TypeTaskClaimed, claimed)
	writeMsg(t, c, protocol.TypeTaskCompleted, completed)
	writeMsg(t, c, protocol.TypeTaskClaimed, claimed)
	writeMsg(t, c, protocol.TypeTaskCompleted, completed)

	fake.waitTerminals(t, 2)
	fake.waitClaimed(t, 2)
	if len(fake.terminals()) != 2 {
		t.Fatalf("terminals %+v", fake.terminals())
	}
	for _, call := range fake.terminals() {
		if call.workerID != "w1" {
			t.Fatalf("worker %s", call.workerID)
		}
		msg, ok := call.msg.(protocol.TaskCompleted)
		if !ok || msg.TaskID != "run_1" || msg.ExecutionID != "exec_1" {
			t.Fatalf("terminal %#v", call.msg)
		}
	}
	// The duplicate must not drop the socket.
	if infos := g.Connected(); len(infos) != 1 || infos[0].WorkerID != "w1" {
		t.Fatalf("connected %+v", infos)
	}
	select {
	case err := <-c.errc:
		t.Fatalf("gateway closed the connection: %v", err)
	default:
	}
}

func TestFailedTerminal(t *testing.T) {
	_, fake, srv := startGateway(t, nil)
	c := dialClient(t, wsURL(srv, "w1"), "worker-key")
	fake.waitConnected(t, 1)
	writeMsg(t, c, protocol.TypeTaskFailed, protocol.TaskFailed{
		TaskID:        "run_1",
		ExecutionID:   "exec_1",
		Message:       "boom",
		FailureReason: "environment_setup_failed",
		ExitCode:      137,
	})
	fake.waitTerminals(t, 1)
	call := fake.terminals()[0]
	msg, ok := call.msg.(protocol.TaskFailed)
	if !ok || call.workerID != "w1" || msg.FailureReason != "environment_setup_failed" || msg.ExitCode != 137 {
		t.Fatalf("%#v", call)
	}
}

func TestRejected(t *testing.T) {
	_, fake, srv := startGateway(t, nil)
	c := dialClient(t, wsURL(srv, "w1"), "worker-key")
	fake.waitConnected(t, 1)

	// A bad frame must not kill the connection; the reject that follows still lands.
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	writeMsg(t, c, protocol.TypeTaskRejected, protocol.TaskRejected{TaskID: "run_1", Reason: "at maximum concurrency"})
	fake.waitRejected(t, 1)
	got := fake.rejectedCalls()
	if len(got) != 1 || got[0].TaskID != "run_1" || got[0].Reason != "at maximum concurrency" {
		t.Fatalf("%+v", got)
	}
}

func TestDisconnectCallsWorkerGone(t *testing.T) {
	g, fake, srv := startGateway(t, nil)
	c := dialClient(t, wsURL(srv, "w1"), "worker-key")
	fake.waitConnected(t, 1)
	infos := g.Connected()
	if len(infos) != 1 || infos[0].ConnID == "" {
		t.Fatalf("%+v", infos)
	}
	id := infos[0].ConnID
	if err := c.conn.Close(); err != nil {
		t.Fatal(err)
	}
	fake.waitGone(t, 1)
	gone := fake.goneIDs()
	if len(gone) != 1 || gone[0] != id {
		t.Fatalf("gone %v want %s", gone, id)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(g.Connected()) != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(g.Connected()) != 0 {
		t.Fatalf("still connected %+v", g.Connected())
	}
}

func TestTwoConnsSameWorkerAssignsOne(t *testing.T) {
	g, fake, srv := startGateway(t, nil)
	c1 := dialClient(t, wsURL(srv, "shared"), "worker-key")
	fake.waitConnected(t, 1)
	c2 := dialClient(t, wsURL(srv, "shared"), "worker-key")
	fake.waitConnected(t, 2)

	infos := g.Connected()
	if len(infos) != 2 || infos[0].ConnID == infos[1].ConnID {
		t.Fatalf("%+v", infos)
	}
	for _, info := range infos {
		if info.WorkerID != "shared" {
			t.Fatalf("%+v", infos)
		}
	}

	if err := g.Assign(context.Background(), "shared", protocol.TaskAssignment{TaskID: "run_1"}); err != nil {
		t.Fatal(err)
	}

	var got int
	deadline := time.After(2 * time.Second)
	for got == 0 {
		select {
		case <-c1.msgs:
			got++
		case <-c2.msgs:
			got++
		case <-deadline:
			t.Fatal("no assignment delivered")
		}
	}
	select {
	case <-c1.msgs:
		got++
	case <-c2.msgs:
		got++
	case <-time.After(150 * time.Millisecond):
	}
	if got != 1 {
		t.Fatalf("delivered to %d connections", got)
	}
	inFlight := 0
	for _, info := range g.Connected() {
		inFlight += info.InFlight
	}
	if inFlight != 1 || len(g.Connected()) != 2 {
		t.Fatalf("connected %+v", g.Connected())
	}
}

func TestCancelSendsTaskCancellation(t *testing.T) {
	g, fake, srv := startGateway(t, nil)
	c := dialClient(t, wsURL(srv, "w1"), "worker-key")
	fake.waitConnected(t, 1)
	if err := g.Assign(context.Background(), "w1", protocol.TaskAssignment{TaskID: "run_9"}); err != nil {
		t.Fatal(err)
	}
	if env := c.readOne(t); env.Type != protocol.TypeTaskAssignment {
		t.Fatalf("preface %s", env.Type)
	}
	if err := g.Cancel(context.Background(), "w1", "run_9"); err != nil {
		t.Fatal(err)
	}
	env := c.readOne(t)
	if env.Type != protocol.TypeTaskCancellation {
		t.Fatalf("type %s body %s", env.Type, env.Data)
	}
	var got protocol.TaskCancellation
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "run_9" {
		t.Fatalf("%+v", got)
	}
	if err := g.Cancel(context.Background(), "missing", "run_9"); err == nil {
		t.Fatal("cancel of unknown worker")
	}
}

func TestAssignLogsEnvKeysNotValues(t *testing.T) {
	const planted = "planted-env-value-9c2e7a"
	var buf lockedBuf
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	g, fake, srv := startGateway(t, log)
	c := dialClient(t, wsURL(srv, "w1"), "worker-key")
	fake.waitConnected(t, 1)

	err := g.Assign(context.Background(), "w1", protocol.TaskAssignment{
		TaskID: "run_1",
		Task:   &protocol.Task{ID: "run_1", Definition: protocol.TaskDefinition{Prompt: "hello"}},
		EnvVars: map[string]string{
			"OPENAI_API_KEY": planted,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := c.readOne(t)
	if !bytes.Contains(env.Data, []byte(planted)) {
		t.Fatal("wire assignment dropped the env value")
	}
	writeMsg(t, c, protocol.TypeTaskCompleted, protocol.TaskCompleted{
		TaskID:  "run_1",
		Message: planted,
	})
	fake.waitTerminals(t, 1)

	text := buf.String()
	if !strings.Contains(text, "OPENAI_API_KEY") {
		t.Fatalf("log missing env key: %s", text)
	}
	if strings.Contains(text, planted) {
		t.Fatalf("log leaked env value: %s", text)
	}
}

func TestReadLimitClosesConnection(t *testing.T) {
	_, fake, srv := startGateway(t, nil)
	c := dialClient(t, wsURL(srv, "w1"), "worker-key")
	fake.waitConnected(t, 1)
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	payload := bytes.Repeat([]byte("x"), (4<<20)+1)
	_ = c.conn.WriteMessage(websocket.TextMessage, payload)
	fake.waitGone(t, 1)
}

func startGateway(t *testing.T, log *slog.Logger) (*Gateway, *fakeSessions, *httptest.Server) {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	fake := &fakeSessions{}
	g := New(testAuth, fake, log)
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return g, fake, srv
}

func testAuth(bearer string) (string, bool) {
	switch bearer {
	case "worker-key":
		return "acme", true
	case "factory-key":
		return "factory", true
	default:
		return "", false
	}
}

func wsURL(srv *httptest.Server, workerID string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/selfhosted/worker/ws?worker_id=" + url.QueryEscape(workerID)
}

func dialStatus(t *testing.T, rawURL, token string) (int, string, error) {
	t.Helper()
	hdr := http.Header{}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(rawURL, hdr)
	if conn != nil {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("close conn: %v", cerr)
		}
	}
	if resp != nil && resp.Body != nil {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close body: %v", cerr)
		}
	}
	if resp == nil {
		return 0, "", err
	}
	return resp.StatusCode, resp.Header.Get("Upgrade"), err
}

type wsClient struct {
	conn  *websocket.Conn
	msgs  chan []byte
	pongs chan string
	errc  chan error
}

func dialClient(t *testing.T, rawURL, token string) *wsClient {
	t.Helper()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+token)
	conn, resp, err := websocket.DefaultDialer.Dial(rawURL, hdr)
	if resp != nil && resp.Body != nil {
		defer func() {
			if cerr := resp.Body.Close(); cerr != nil {
				t.Errorf("close body: %v", cerr)
			}
		}()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &wsClient{
		conn:  conn,
		msgs:  make(chan []byte, 16),
		pongs: make(chan string, 4),
		errc:  make(chan error, 1),
	}
	conn.SetPongHandler(func(appData string) error {
		c.pongs <- appData
		return nil
	})
	go c.read()
	t.Cleanup(func() {
		if cerr := conn.Close(); cerr != nil && !strings.Contains(cerr.Error(), "use of closed") {
			t.Logf("close websocket: %v", cerr)
		}
	})
	return c
}

func (c *wsClient) read() {
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			c.errc <- err
			return
		}
		c.msgs <- msg
	}
}

func (c *wsClient) readOne(t *testing.T) protocol.Envelope {
	t.Helper()
	select {
	case raw := <-c.msgs:
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("envelope: %v", err)
		}
		return env
	case err := <-c.errc:
		t.Fatalf("read: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
	return protocol.Envelope{}
}

func writeMsg(t *testing.T, c *wsClient, typ string, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(protocol.Envelope{Type: typ, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatal(err)
	}
}

type seenCall struct {
	workerID string
	at       time.Time
}

type terminalCall struct {
	workerID string
	msg      any
}

type fakeSessions struct {
	mu       sync.Mutex
	conns    []WorkerInfo
	seen     []seenCall
	claimed  []protocol.TaskClaimed
	terms    []terminalCall
	rejected []protocol.TaskRejected
	gone     []string
}

func (f *fakeSessions) WorkerConnected(_ context.Context, w WorkerInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns = append(f.conns, w)
	return nil
}

func (f *fakeSessions) WorkerSeen(_ context.Context, workerID string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, seenCall{workerID: workerID, at: at})
}

func (f *fakeSessions) Claimed(_ context.Context, m protocol.TaskClaimed) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimed = append(f.claimed, m)
	return nil
}

func (f *fakeSessions) Terminal(_ context.Context, workerID string, m any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terms = append(f.terms, terminalCall{workerID: workerID, msg: m})
	return nil
}

func (f *fakeSessions) Rejected(_ context.Context, m protocol.TaskRejected) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejected = append(f.rejected, m)
	return nil
}

func (f *fakeSessions) WorkerGone(_ context.Context, connID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gone = append(f.gone, connID)
}

func (f *fakeSessions) connectedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns)
}

func (f *fakeSessions) connected() []WorkerInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]WorkerInfo, len(f.conns))
	copy(out, f.conns)
	return out
}

func (f *fakeSessions) seenCalls() []seenCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]seenCall, len(f.seen))
	copy(out, f.seen)
	return out
}

func (f *fakeSessions) terminals() []terminalCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]terminalCall, len(f.terms))
	copy(out, f.terms)
	return out
}

func (f *fakeSessions) rejectedCalls() []protocol.TaskRejected {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.TaskRejected, len(f.rejected))
	copy(out, f.rejected)
	return out
}

func (f *fakeSessions) goneIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.gone))
	copy(out, f.gone)
	return out
}

func (f *fakeSessions) waitConnected(t *testing.T, n int) {
	t.Helper()
	f.wait(t, n, f.connectedCount, "connected")
}

func (f *fakeSessions) waitTerminals(t *testing.T, n int) {
	t.Helper()
	f.wait(t, n, func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.terms)
	}, "terminals")
}

func (f *fakeSessions) waitClaimed(t *testing.T, n int) {
	t.Helper()
	f.wait(t, n, func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.claimed)
	}, "claimed")
}

func (f *fakeSessions) waitRejected(t *testing.T, n int) {
	t.Helper()
	f.wait(t, n, func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.rejected)
	}, "rejected")
}

func (f *fakeSessions) waitGone(t *testing.T, n int) {
	t.Helper()
	f.wait(t, n, func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.gone)
	}, "gone")
}

func (f *fakeSessions) wait(t *testing.T, n int, count func() int, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if count() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s = %d, want >= %d", name, count(), n)
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *lockedBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *lockedBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
