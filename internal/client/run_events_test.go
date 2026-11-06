package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunEventsHeartbeatIsLiveness(t *testing.T) {
	for _, caller := range []string{"watch", "stream"} {
		t.Run(caller, func(t *testing.T) {
			var conns atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-key" {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				if r.Header.Get("Accept") != "text/event-stream" {
					http.Error(w, "bad accept", http.StatusBadRequest)
					return
				}
				if conns.Add(1) != 1 {
					http.Error(w, "reconnected while heartbeats flowed", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				// Same comment bytes as internal/events handler heartbeats (": ping").
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				deadline := time.NewTimer(2500 * time.Millisecond)
				defer deadline.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-deadline.C:
						writeSSE(t, w, "1", "run.terminal", `{"status":"succeeded"}`)
						if flusher != nil {
							flusher.Flush()
						}
						return
					case <-ticker.C:
						if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
							return
						}
						if flusher != nil {
							flusher.Flush()
						}
					}
				}
			}))
			t.Cleanup(srv.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			c := &Client{
				BaseURL:    srv.URL,
				APIKey:     "test-key",
				Backoff:    func(int) time.Duration { return 0 },
				StreamIdle: time.Second,
			}
			if caller == "watch" {
				code, err := c.Watch(ctx, "run_hb", nil, io.Discard)
				if err != nil {
					t.Fatal(err)
				}
				if code != 0 {
					t.Fatalf("exit %d", code)
				}
			} else {
				var got int
				err := c.StreamRunEvents(ctx, "run_hb", func(ev Event) error {
					if ev.Type == "run.terminal" {
						got++
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if got != 1 {
					t.Fatalf("run.terminal deliveries %d", got)
				}
			}
			if conns.Load() != 1 {
				t.Fatalf("connections %d", conns.Load())
			}
		})
	}
}

func TestRunEventsIdleReconnectCursor(t *testing.T) {
	for _, caller := range []string{"watch", "stream"} {
		t.Run(caller, func(t *testing.T) {
			var mu sync.Mutex
			var cursors []string
			var conns atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := conns.Add(1)
				mu.Lock()
				cursors = append(cursors, r.Header.Get("Last-Event-ID")+"/"+r.URL.Query().Get("from_seq"))
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				if n == 1 {
					writeSSE(t, w, "1", "message.delta", `{"role":"assistant","text":"one"}`)
					if flusher != nil {
						flusher.Flush()
					}
					<-r.Context().Done()
					return
				}
				writeSSE(t, w, "1", "message.delta", `{"role":"assistant","text":"one"}`)
				writeSSE(t, w, "2", "run.terminal", `{"status":"succeeded","summary":"done"}`)
				if flusher != nil {
					flusher.Flush()
				}
			}))
			t.Cleanup(srv.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c := &Client{
				BaseURL:    srv.URL,
				APIKey:     "test-key",
				Backoff:    func(int) time.Duration { return 0 },
				StreamIdle: 150 * time.Millisecond,
			}
			if caller == "watch" {
				var out strings.Builder
				code, err := c.Watch(ctx, "run_idle", nil, &out)
				if err != nil {
					t.Fatal(err)
				}
				if code != 0 {
					t.Fatalf("exit %d", code)
				}
				text := out.String()
				if strings.Count(text, "message.delta role=assistant text=one") != 1 {
					t.Fatalf("duplicate or missing line in %q", text)
				}
				if strings.Count(text, "terminal status=succeeded summary=done") != 1 {
					t.Fatalf("terminal count in %q", text)
				}
			} else {
				var ids []string
				err := c.StreamRunEvents(ctx, "run_idle", func(ev Event) error {
					ids = append(ids, ev.ID+":"+ev.Type)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if strings.Join(ids, ",") != "1:message.delta,2:run.terminal" {
					t.Fatalf("events %q", ids)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(cursors) < 2 || cursors[0] != "/" || cursors[1] != "1/1" {
				t.Fatalf("cursors %q", cursors)
			}
		})
	}
}

func TestRunEventsSharedPolicyMatrix(t *testing.T) {
	t.Run("headers_and_seed", func(t *testing.T) {
		from := int64(7)
		rt := &scriptRT{fns: []roundTripFunc{sseBody("id: 8\nevent: run.terminal\ndata: {\"status\":\"succeeded\"}\n\n")}}
		c := scriptedClient(rt)
		if _, err := c.Watch(context.Background(), "run/x", &from, nil); err != nil {
			t.Fatal(err)
		}
		req := rt.requests[0]
		if req.URL.Path != "/api/v1/agent/runs/run/x/events" {
			t.Fatalf("path %s", req.URL.Path)
		}
		if req.URL.Query().Get("from_seq") != "7" {
			t.Fatalf("from_seq %q", req.URL.Query().Get("from_seq"))
		}
		if req.Header.Get("Authorization") != "Bearer k" || req.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("headers %v", req.Header)
		}
		if req.Header.Get("Last-Event-ID") != "" {
			t.Fatalf("Last-Event-ID %q", req.Header.Get("Last-Event-ID"))
		}

		rt = &scriptRT{fns: []roundTripFunc{sseBody("id: 1\nevent: run.terminal\ndata: {\"status\":\"succeeded\"}\n\n")}}
		c = scriptedClient(rt)
		if err := c.StreamRunEvents(context.Background(), "run_x", nil); err != nil {
			t.Fatal(err)
		}
		if rt.requests[0].URL.RawQuery != "" {
			t.Fatalf("stream seeded query %q", rt.requests[0].URL.RawQuery)
		}
	})

	t.Run("transport_retries_with_backoff", func(t *testing.T) {
		for _, caller := range []string{"watch", "stream"} {
			t.Run(caller, func(t *testing.T) {
				var attempts []int
				rt := &scriptRT{fns: []roundTripFunc{
					func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: refused") },
					func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: refused") },
					sseBody("id: 1\nevent: run.terminal\ndata: {\"status\":\"succeeded\"}\n\n"),
				}}
				c := scriptedClient(rt)
				c.Backoff = func(n int) time.Duration {
					attempts = append(attempts, n)
					return 0
				}
				var err error
				if caller == "watch" {
					_, err = c.Watch(context.Background(), "run_x", nil, io.Discard)
				} else {
					err = c.StreamRunEvents(context.Background(), "run_x", nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				if strings.Join(ints(attempts), ",") != "1,2" {
					t.Fatalf("backoff %v", attempts)
				}
				if len(rt.requests) != 3 {
					t.Fatalf("requests %d", len(rt.requests))
				}
			})
		}
	})

	t.Run("transport_uses_context_error", func(t *testing.T) {
		for _, caller := range []string{"watch", "stream"} {
			t.Run(caller, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				rt := &scriptRT{fns: []roundTripFunc{func(r *http.Request) (*http.Response, error) {
					cancel()
					return nil, errors.New("dial tcp: refused")
				}}}
				c := scriptedClient(rt)
				var err error
				if caller == "watch" {
					_, err = c.Watch(ctx, "run_x", nil, io.Discard)
				} else {
					err = c.StreamRunEvents(ctx, "run_x", nil)
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err %v", err)
				}
				if len(rt.requests) != 1 {
					t.Fatalf("requests %d", len(rt.requests))
				}
			})
		}
	})

	t.Run("problem_does_not_retry", func(t *testing.T) {
		for _, caller := range []string{"watch", "stream"} {
			t.Run(caller, func(t *testing.T) {
				rt := &scriptRT{fns: []roundTripFunc{statusBody(http.StatusNotFound, `{"detail":"missing run"}`)}}
				c := scriptedClient(rt)
				var err error
				if caller == "watch" {
					_, err = c.Watch(context.Background(), "run_x", nil, io.Discard)
				} else {
					err = c.StreamRunEvents(context.Background(), "run_x", nil)
				}
				if err == nil || !strings.Contains(err.Error(), "missing run") {
					t.Fatalf("err %v", err)
				}
				if len(rt.requests) != 1 {
					t.Fatalf("requests %d", len(rt.requests))
				}
			})
		}
	})

	t.Run("problem_body_is_capped", func(t *testing.T) {
		payload := strings.Repeat("x", 2<<20) + "END"
		body := &countBody{r: strings.NewReader(payload)}
		rt := &scriptRT{fns: []roundTripFunc{func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     make(http.Header),
				Body:       io.NopCloser(body),
			}, nil
		}}}
		c := scriptedClient(rt)
		_, err := c.Watch(context.Background(), "run_x", nil, io.Discard)
		if err == nil || strings.Contains(err.Error(), "END") {
			t.Fatalf("err %v", err)
		}
		if body.n != 1<<20 {
			t.Fatalf("read %d", body.n)
		}
	})

	t.Run("problem_read_error", func(t *testing.T) {
		rt := &scriptRT{fns: []roundTripFunc{func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Header:     make(http.Header),
				Body:       io.NopCloser(errReader{err: errors.New("body broke")}),
			}, nil
		}}}
		c := scriptedClient(rt)
		_, err := c.Watch(context.Background(), "run_x", nil, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "body broke") {
			t.Fatalf("err %v", err)
		}
		if len(rt.requests) != 1 {
			t.Fatalf("requests %d", len(rt.requests))
		}
	})

	t.Run("read_error_policy", func(t *testing.T) {
		boom := func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(errReader{err: errors.New("boom")})}, nil
		}
		rt := &scriptRT{fns: []roundTripFunc{boom}}
		c := scriptedClient(rt)
		_, err := c.Watch(context.Background(), "run_x", nil, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("watch err %v", err)
		}
		if len(rt.requests) != 1 {
			t.Fatalf("watch requests %d", len(rt.requests))
		}

		rt = &scriptRT{fns: []roundTripFunc{
			boom,
			sseBody("id: 1\nevent: run.terminal\ndata: {\"status\":\"succeeded\"}\n\n"),
		}}
		c = scriptedClient(rt)
		if err := c.StreamRunEvents(context.Background(), "run_x", nil); err != nil {
			t.Fatal(err)
		}
		if len(rt.requests) != 2 {
			t.Fatalf("stream requests %d", len(rt.requests))
		}
	})

	t.Run("yield_error_reconnects_and_dedupes", func(t *testing.T) {
		rt := &scriptRT{fns: []roundTripFunc{
			sseBody("id: 1\nevent: message.delta\ndata: {\"role\":\"assistant\",\"text\":\"one\"}\n\n"),
			sseBody("id: 1\nevent: message.delta\ndata: {\"role\":\"assistant\",\"text\":\"one\"}\n\nid: 2\nevent: run.terminal\ndata: {\"status\":\"succeeded\"}\n\n"),
		}}
		c := scriptedClient(rt)
		var ids []string
		err := c.StreamRunEvents(context.Background(), "run_x", func(ev Event) error {
			if ev.ID == "1" {
				return errors.New("yield broke")
			}
			ids = append(ids, ev.ID)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(ids, ",") != "2" {
			t.Fatalf("yielded %q", ids)
		}
		if len(rt.requests) != 2 {
			t.Fatalf("requests %d", len(rt.requests))
		}
		if rt.requests[1].Header.Get("Last-Event-ID") != "1" || rt.requests[1].URL.Query().Get("from_seq") != "1" {
			t.Fatalf("resume %s %s", rt.requests[1].Header.Get("Last-Event-ID"), rt.requests[1].URL.RawQuery)
		}
	})

	t.Run("render_error_does_not_reconnect", func(t *testing.T) {
		rt := &scriptRT{fns: []roundTripFunc{sseBody("id: 1\nevent: message.delta\ndata: {\"role\":\"assistant\",\"text\":\"one\"}\n\n")}}
		c := scriptedClient(rt)
		_, err := c.Watch(context.Background(), "run_x", nil, errWriter{err: errors.New("stdout broke")})
		if err == nil || !strings.Contains(err.Error(), "stdout broke") {
			t.Fatalf("err %v", err)
		}
		if len(rt.requests) != 1 {
			t.Fatalf("requests %d", len(rt.requests))
		}
	})

	t.Run("context_already_cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rt := &scriptRT{}
		c := scriptedClient(rt)
		if _, err := c.Watch(ctx, "run_x", nil, io.Discard); !errors.Is(err, context.Canceled) {
			t.Fatalf("watch %v", err)
		}
		if err := c.StreamRunEvents(ctx, "run_x", nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("stream %v", err)
		}
		if len(rt.requests) != 0 {
			t.Fatalf("requests %d", len(rt.requests))
		}
	})

	t.Run("dedupe_across_reconnect", func(t *testing.T) {
		rt := &scriptRT{fns: []roundTripFunc{
			sseBody("id: 1\nevent: message.delta\ndata: {\"role\":\"assistant\",\"text\":\"one\"}\n\nid: 2\nevent: message.delta\ndata: {\"role\":\"assistant\",\"text\":\"two\"}\n\n"),
			sseBody("id: 2\nevent: message.delta\ndata: {\"role\":\"assistant\",\"text\":\"two\"}\n\nid: 3\nevent: run.terminal\ndata: {\"status\":\"cancelled\"}\n\n"),
		}}
		c := scriptedClient(rt)
		var out strings.Builder
		code, err := c.Watch(context.Background(), "run_x", nil, &out)
		if err != nil {
			t.Fatal(err)
		}
		if code != 2 {
			t.Fatalf("exit %d", code)
		}
		text := out.String()
		if strings.Count(text, "text=one") != 1 || strings.Count(text, "text=two") != 1 {
			t.Fatalf("lines %q", text)
		}
		if rt.requests[1].Header.Get("Last-Event-ID") != "2" || rt.requests[1].URL.Query().Get("from_seq") != "2" {
			t.Fatalf("resume %s %s", rt.requests[1].Header.Get("Last-Event-ID"), rt.requests[1].URL.RawQuery)
		}
	})

	t.Run("bad_endpoint", func(t *testing.T) {
		c := &Client{BaseURL: "://bad", APIKey: "k", Backoff: func(int) time.Duration { return 0 }}
		if _, err := c.Watch(context.Background(), "run_x", nil, io.Discard); err == nil {
			t.Fatal("watch accepted a bad base")
		}
		if err := c.StreamRunEvents(context.Background(), "run_x", nil); err == nil {
			t.Fatal("stream accepted a bad base")
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

type scriptRT struct {
	mu       sync.Mutex
	i        int
	fns      []roundTripFunc
	requests []*http.Request
}

func (s *scriptRT) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Clone(context.Background()))
	if s.i >= len(s.fns) {
		return nil, errors.New("unexpected request")
	}
	fn := s.fns[s.i]
	s.i++
	return fn(r)
}

func scriptedClient(rt *scriptRT) *Client {
	return &Client{
		BaseURL: "http://factory.example",
		APIKey:  "k",
		HTTP:    &http.Client{Transport: rt},
		Backoff: func(int) time.Duration { return 0 },
	}
}

func sseBody(body string) roundTripFunc {
	return statusBody(http.StatusOK, body)
}

func statusBody(status int, body string) roundTripFunc {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
}

type countBody struct {
	r io.Reader
	n int
}

func (c *countBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

type errWriter struct{ err error }

func (e errWriter) Write([]byte) (int, error) { return 0, e.err }

func ints(in []int) []string {
	out := make([]string, len(in))
	for i, n := range in {
		out[i] = fmt.Sprintf("%d", n)
	}
	return out
}
