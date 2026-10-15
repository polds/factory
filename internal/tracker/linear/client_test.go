package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testKey = "lin_api_test_DO_NOT_LEAK"

func TestCreateAndUpdateComment(t *testing.T) {
	const (
		issueID   = "11111111-1111-4111-8111-111111111111"
		commentID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	)
	var mu sync.Mutex
	var calls []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, err := record(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		calls = append(calls, rec)
		mu.Unlock()
		switch {
		case strings.Contains(rec.Query, "commentCreate"):
			writeJSON(w, `{"data":{"commentCreate":{"success":true,"comment":{"id":"`+commentID+`"}}}}`)
		case strings.Contains(rec.Query, "commentUpdate"):
			writeJSON(w, `{"data":{"commentUpdate":{"success":true,"comment":{"id":"`+commentID+`"}}}}`)
		default:
			http.Error(w, "unknown operation", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "  "+testKey+"  ")
	client.HTTP = srv.Client()
	id, err := client.CreateComment(context.Background(), "  "+issueID+"  ", "hello **world**")
	if err != nil {
		t.Fatal(err)
	}
	if id != commentID {
		t.Fatalf("id = %s", id)
	}
	if err := client.UpdateComment(context.Background(), commentID, "hello edited"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	create := calls[0]
	if create.Auth != testKey {
		t.Fatalf("auth = %q", create.Auth)
	}
	if create.ContentType != "application/json" {
		t.Fatalf("content type = %s", create.ContentType)
	}
	if strings.Contains(create.Body, testKey) {
		t.Fatal("create body contains the API key")
	}
	if create.Query != queryCommentCreate {
		t.Fatalf("query = %s", create.Query)
	}
	input := create.Vars["input"].(map[string]any)
	if input["issueId"] != issueID || input["body"] != "hello **world**" {
		t.Fatalf("create vars = %#v", create.Vars)
	}
	update := calls[1]
	if update.Query != queryCommentUpdate {
		t.Fatalf("update query = %s", update.Query)
	}
	if update.Vars["id"] != commentID {
		t.Fatalf("update id = %#v", update.Vars["id"])
	}
	updated := update.Vars["input"].(map[string]any)
	if updated["body"] != "hello edited" {
		t.Fatalf("update body = %#v", updated["body"])
	}
}

func TestGraphQLAndHTTPErrorsRedactKey(t *testing.T) {
	const leaked = "bad key " + testKey
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "graphql",
			status: http.StatusUnauthorized,
			body:   `{"errors":[{"message":"` + leaked + `"}]}`,
			want:   "Authentication",
		},
		{
			name:   "status",
			status: http.StatusTooManyRequests,
			body:   `{"errors":[{"message":"ratelimited"}]}`,
			want:   "ratelimited",
		},
		{
			name:   "plain",
			status: http.StatusBadGateway,
			body:   leaked,
			want:   "bad key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			client := New(srv.URL, testKey)
			client.HTTP = srv.Client()
			_, err := client.CreateComment(context.Background(), "issue", "body")
			if err == nil {
				t.Fatal("expected error")
			}
			var api *APIError
			if !errors.As(err, &api) {
				t.Fatalf("err = %T %v", err, err)
			}
			if api.Status != tc.status {
				t.Fatalf("status = %d", api.Status)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Fatalf("error contains key: %s", err.Error())
			}
			if tc.name == "graphql" && !strings.Contains(err.Error(), "[REDACTED]") {
				t.Fatalf("key was not redacted: %s", err.Error())
			}
			if tc.name != "graphql" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %s", err.Error())
			}
		})
	}
}

func TestGraphQLErrorWith200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"errors":[{"message":"nope `+testKey+`"}],"data":null}`)
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, testKey)
	client.HTTP = srv.Client()
	_, err := client.CreateComment(context.Background(), "issue", "body")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("err = %s", err)
	}
}

func TestUnsuccessfulMutation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, err := record(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		op := "commentCreate"
		if strings.Contains(rec.Query, "commentUpdate") {
			op = "commentUpdate"
		}
		writeJSON(w, `{"data":{"`+op+`":{"success":false,"comment":null}}}`)
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, testKey)
	client.HTTP = srv.Client()
	if _, err := client.CreateComment(context.Background(), "issue", "body"); err == nil {
		t.Fatal("create succeeded")
	}
	if err := client.UpdateComment(context.Background(), "comment", "body"); err == nil {
		t.Fatal("update succeeded")
	}
}

func TestEmptyInputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server was called")
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, testKey)
	client.HTTP = srv.Client()
	if _, err := client.CreateComment(context.Background(), "  ", "body"); !errors.Is(err, ErrEmptyIssue) {
		t.Fatalf("create err = %v", err)
	}
	if err := client.UpdateComment(context.Background(), "", "body"); !errors.Is(err, ErrEmptyComment) {
		t.Fatalf("update err = %v", err)
	}
	bare := New(srv.URL, " ")
	if _, err := bare.CreateComment(context.Background(), "issue", "body"); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("key err = %v", err)
	}
	if _, err := (*Client)(nil).CreateComment(context.Background(), "issue", "body"); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("nil err = %v", err)
	}
}

func TestStringAndEndpointRedactKey(t *testing.T) {
	client := New("http://127.0.0.1/"+testKey, testKey)
	if strings.Contains(client.String(), testKey) {
		t.Fatalf("String leaked the key: %s", client)
	}
	if !strings.Contains(fmt.Sprint(client), "linear.Client") {
		t.Fatalf("String = %s", client)
	}
	if _, err := client.CreateComment(context.Background(), "issue", "body"); err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("err = %v", err)
	}
	if _, err := New("not a url", testKey).CreateComment(context.Background(), "issue", "body"); err == nil {
		t.Fatal("relative endpoint was accepted")
	}
}

func TestDefaultEndpoint(t *testing.T) {
	client := New("", testKey)
	got, err := client.resolveEndpoint()
	if err != nil || got != defaultEndpoint {
		t.Fatalf("endpoint = %s, %v", got, err)
	}
	if strings.Contains(client.String(), testKey) {
		t.Fatal("default string contains the key")
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	otherHit := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHit = true
		if r.Header.Get("Authorization") != "" {
			t.Error("authorization reached the redirect target")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, testKey)
	_, err := client.CreateComment(context.Background(), "issue", "body")
	if err == nil {
		t.Fatal("redirect was treated as success")
	}
	if otherHit {
		t.Fatal("client followed the redirect")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error contains key: %s", err)
	}
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusFound {
		t.Fatalf("err = %v", err)
	}
}

func TestCanceledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, testKey)
	client.HTTP = srv.Client()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.CreateComment(ctx, "issue", "body")
	if err == nil {
		t.Fatal("canceled context succeeded")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error contains key: %s", err)
	}
}

func TestCreateCommentConcurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != testKey {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		writeJSON(w, `{"data":{"commentCreate":{"success":true,"comment":{"id":"cmt"}}}}`)
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, testKey)
	client.HTTP = srv.Client()

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			id, err := client.CreateComment(context.Background(), "issue", "body")
			if err != nil {
				errCh <- err
				return
			}
			if id != "cmt" {
				errCh <- errors.New("bad id")
			}
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

type recorded struct {
	Auth        string
	ContentType string
	Body        string
	Query       string
	Vars        map[string]any
}

func record(r *http.Request) (recorded, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return recorded{}, err
	}
	var parsed struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return recorded{}, err
	}
	return recorded{
		Auth:        r.Header.Get("Authorization"),
		ContentType: r.Header.Get("Content-Type"),
		Body:        string(body),
		Query:       parsed.Query,
		Vars:        parsed.Variables,
	}, nil
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}
