package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type ghCall struct {
	method  string
	path    string
	auth    string
	accept  string
	version string
	body    []byte
}

func TestPullRequestCalls(t *testing.T) {
	secretValue := testToken
	var got ghCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := bytes.Buffer{}
		if _, err := buf.ReadFrom(r.Body); err != nil {
			t.Errorf("read body: %v", err)
		}
		got = ghCall{
			method:  r.Method,
			path:    r.URL.Path,
			auth:    r.Header.Get("Authorization"),
			accept:  r.Header.Get("Accept"),
			version: r.Header.Get("X-GitHub-Api-Version"),
			body:    buf.Bytes(),
		}
		switch r.URL.Path {
		case "/repos/acme/app/pulls":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"number":4,"html_url":"https://github.com/acme/app/pull/4"}`))
		case "/repos/acme/app/pulls/4/requested_reviewers", "/repos/acme/app/issues/4/labels":
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{
		AppID:   testAppID,
		Key:     testKey(t),
		BaseURL: srv.URL,
		Log:     slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	tok := Token{value: secret(secretValue), expiresAt: time.Now().Add(time.Hour)}
	ctx := context.Background()

	number, url, err := c.CreatePullRequest(ctx, tok, "acme/app", "feature", "main", "Title", "Body")
	if err != nil {
		t.Fatal(err)
	}
	if number != 4 || url != "https://github.com/acme/app/pull/4" {
		t.Fatalf("created %d %s", number, url)
	}
	assertCall(t, got, http.MethodPost, "/repos/acme/app/pulls", secretValue)
	var prBody struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(got.body, &prBody); err != nil {
		t.Fatal(err)
	}
	if prBody.Title != "Title" || prBody.Head != "feature" || prBody.Base != "main" || prBody.Body != "Body" {
		t.Fatalf("pr body %+v", prBody)
	}

	if err := c.RequestReviewers(ctx, tok, "acme/app", 4, []string{"ada", "grace"}); err != nil {
		t.Fatal(err)
	}
	assertCall(t, got, http.MethodPost, "/repos/acme/app/pulls/4/requested_reviewers", secretValue)
	var reviewers struct {
		Reviewers []string `json:"reviewers"`
	}
	if err := json.Unmarshal(got.body, &reviewers); err != nil {
		t.Fatal(err)
	}
	if strings.Join(reviewers.Reviewers, ",") != "ada,grace" {
		t.Fatalf("reviewers %+v", reviewers)
	}

	if err := c.AddLabels(ctx, tok, "acme/app", 4, []string{"bug"}); err != nil {
		t.Fatal(err)
	}
	assertCall(t, got, http.MethodPost, "/repos/acme/app/issues/4/labels", secretValue)
	var labels struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(got.body, &labels); err != nil {
		t.Fatal(err)
	}
	if len(labels.Labels) != 1 || labels.Labels[0] != "bug" {
		t.Fatalf("labels %+v", labels)
	}
}

func TestPullRequestErrorRedactsToken(t *testing.T) {
	secretValue := testToken
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"denied ` + secretValue + `"}`))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{
		AppID:   testAppID,
		Key:     testKey(t),
		BaseURL: srv.URL,
		Log:     slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	tok := Token{value: secret(secretValue), expiresAt: time.Now().Add(time.Hour)}
	_, _, err = c.CreatePullRequest(context.Background(), tok, "acme/app", "feature", "main", "Title", "Body")
	if err == nil || strings.Contains(err.Error(), secretValue) {
		t.Fatalf("error %v", err)
	}
	if !strings.Contains(err.Error(), tokenRedacted) {
		t.Fatalf("error %v", err)
	}
}

func TestPullTokenRedacts(t *testing.T) {
	secretValue := testToken
	tok := Token{value: secret(secretValue), expiresAt: time.Now().Add(time.Hour)}
	rendered := []string{
		fmt.Sprintf("%v", tok),
		fmt.Sprintf("%+v", tok),
		fmt.Sprintf("%#v", tok),
		fmt.Sprintf("%v", &tok),
		fmt.Sprintf("%+v", &tok),
		fmt.Sprintf("%#v", &tok),
		tok.String(),
		tok.GoString(),
	}
	for _, text := range rendered {
		if strings.Contains(text, secretValue) {
			t.Fatalf("rendered %q", text)
		}
		if text != "github.Token{}" && !strings.Contains(text, tokenRedacted) {
			t.Fatalf("rendered %q", text)
		}
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	log.Info("token", "value", tok, "ptr", &tok)
	if strings.Contains(buf.String(), secretValue) || !strings.Contains(buf.String(), tokenRedacted) {
		t.Fatalf("slog %s", buf.String())
	}
	raw, err := json.Marshal(tok)
	if err == nil || strings.Contains(string(raw), secretValue) || strings.Contains(err.Error(), secretValue) {
		t.Fatalf("json %s %v", raw, err)
	}
	wrapped, err := json.Marshal(struct {
		Token Token `json:"token"`
	}{Token: tok})
	if err == nil || strings.Contains(string(wrapped), secretValue) || strings.Contains(err.Error(), secretValue) {
		t.Fatalf("wrapped %s %v", wrapped, err)
	}
}

func TestPullsExportNoMerge(t *testing.T) {
	check := func(name string) {
		t.Helper()
		if ast.IsExported(name) && strings.Contains(name, "Merge") {
			t.Fatalf("exported name %s contains Merge", name)
		}
	}
	typ := reflect.TypeOf(&Client{})
	for i := 0; i < typ.NumMethod(); i++ {
		check(typ.Method(i).Name)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				check(node.Name.Name)
			case *ast.TypeSpec:
				check(node.Name.Name)
			case *ast.ValueSpec:
				for _, name := range node.Names {
					check(name.Name)
				}
			case *ast.Field:
				for _, name := range node.Names {
					check(name.Name)
				}
			}
			return true
		})
	}
}

func TestDraftPullAndHeadPrefix(t *testing.T) {
	secretValue := testToken
	var calls []string
	var createBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := bytes.Buffer{}
		_, _ = buf.ReadFrom(r.Body)
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if r.URL.Query().Has("head") {
			t.Errorf("exact head filter %s", r.URL.RawQuery)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/app/pulls":
			createBody = append([]byte(nil), buf.Bytes()...)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"number":9,"html_url":"https://github.com/acme/app/pull/9","draft":true,"state":"open","head":{"ref":"factory/dreaming-454-inv_abcd","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/pulls/9":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"number":9,"html_url":"https://github.com/acme/app/pull/9","draft":true,"state":"open","head":{"ref":"factory/dreaming-454-inv_abcd","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/pulls":
			w.Header().Set("Content-Type", "application/json")
			page := r.URL.Query().Get("page")
			if page == "" || page == "1" {
				var batch bytes.Buffer
				batch.WriteByte('[')
				for i := 0; i < 100; i++ {
					if i > 0 {
						batch.WriteByte(',')
					}
					_, _ = fmt.Fprintf(&batch, `{"number":%d,"html_url":"https://github.com/acme/app/pull/%d","draft":false,"state":"closed","head":{"ref":"other-%d","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`, i+1, i+1, i)
				}
				batch.WriteByte(']')
				_, _ = w.Write(batch.Bytes())
				return
			}
			_, _ = w.Write([]byte(`[
				{"number":150,"html_url":"https://github.com/acme/app/pull/150","draft":false,"state":"closed","head":{"ref":"refs/heads/factory/dreaming-454-nope","sha":"cccccccccccccccccccccccccccccccccccccccc"}},
				{"number":151,"html_url":"https://github.com/acme/app/pull/151","draft":false,"state":"closed","head":{"ref":"factory/dreaming-454-old","sha":"dddddddddddddddddddddddddddddddddddddddd"}}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{AppID: testAppID, Key: testKey(t), BaseURL: srv.URL, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	tok := Token{value: secret(secretValue), expiresAt: time.Now().Add(time.Hour)}
	got, err := c.CreateDraftPullRequest(context.Background(), tok, "acme/app", "factory/dreaming-454-inv_abcd", "main", "Title", "Body")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Draft || got.Number != 9 || got.HeadSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || got.HeadRef != "factory/dreaming-454-inv_abcd" {
		t.Fatalf("draft %+v", got)
	}
	if !bytes.Contains(createBody, []byte(`"draft":true`)) {
		t.Fatalf("create body %s", createBody)
	}
	read, err := c.GetPullRequest(context.Background(), tok, "acme/app", 9)
	if err != nil || !read.Draft || read.HeadSHA != got.HeadSHA {
		t.Fatalf("get %+v %v", read, err)
	}
	listed, err := c.ListPullsByHeadPrefix(context.Background(), tok, "acme/app", "factory/dreaming-454-")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Number != 151 || listed[0].State != "closed" {
		t.Fatalf("listed %+v", listed)
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := bytes.Buffer{}
		_, _ = buf.ReadFrom(r.Body)
		if bytes.Contains(buf.Bytes(), []byte(`"draft"`)) {
			t.Fatalf("CreatePullRequest sent draft %s", buf.Bytes())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":3,"html_url":"https://github.com/acme/app/pull/3"}`))
	}))
	t.Cleanup(plain.Close)
	plainClient, err := New(Config{AppID: testAppID, Key: testKey(t), BaseURL: plain.URL, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plainClient.CreatePullRequest(context.Background(), tok, "acme/app", "feature", "main", "Title", "Body"); err != nil {
		t.Fatal(err)
	}
}

func TestListPullsNon200(t *testing.T) {
	secretValue := testToken
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"denied ` + secretValue + `"}`))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{AppID: testAppID, Key: testKey(t), BaseURL: srv.URL, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	tok := Token{value: secret(secretValue), expiresAt: time.Now().Add(time.Hour)}
	_, err = c.ListPullsByHeadPrefix(context.Background(), tok, "acme/app", "factory/dreaming-454-")
	if err == nil || strings.Contains(err.Error(), secretValue) {
		t.Fatal(err)
	}
	_, err = c.ListPullsByHeadPrefix(context.Background(), tok, "acme/app", "refs/heads/factory/dreaming-454-")
	if err == nil {
		t.Fatal("refs/heads prefix accepted")
	}
}

func assertCall(t *testing.T, got ghCall, method, path, secret string) {
	t.Helper()
	if got.method != method || got.path != path {
		t.Fatalf("call %s %s", got.method, got.path)
	}
	if got.auth != "Bearer "+secret {
		t.Fatal("authorization header mismatch")
	}
	if got.accept != "application/vnd.github+json" || got.version != apiVersion {
		t.Fatalf("headers accept %s version %s", got.accept, got.version)
	}
}
