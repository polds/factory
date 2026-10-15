package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListDeliveriesUnmatchedOmitsBody(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"dlv_1","provider":"github","delivery_id":"d1","status":"unmatched","matched_automations":[]}]`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	rows, err := c.ListDeliveries(context.Background(), "fac/1", DeliveryFilter{Unmatched: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "dlv_1" || rows[0].Status != "unmatched" {
		t.Fatalf("%+v", rows)
	}
	if gotPath != "/api/v1/factory/fac%2F1/deliveries" {
		t.Fatalf("path %s", gotPath)
	}
	if !strings.Contains(gotQuery, "status=unmatched") || strings.Contains(gotQuery, "include=") {
		t.Fatalf("query %s", gotQuery)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth %s", gotAuth)
	}
}

func TestReplayDoesNotSendSkipVerify(t *testing.T) {
	var gotPath, gotQuery string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.RawQuery
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read: %v", err)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"dlv_1","provider":"github","delivery_id":"d1","status":"received","matched_automations":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	row, err := c.ReplayDelivery(context.Background(), "dlv_1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "received" || gotPath != "/api/v1/deliveries/dlv_1:replay" || gotQuery != "" {
		t.Fatalf("row %+v path %s query %s", row, gotPath, gotQuery)
	}
	if strings.Contains(string(gotBody), "skip") || strings.Contains(string(gotBody), "verify") {
		t.Fatalf("body %s", gotBody)
	}
}

func TestSchedulePauseResumeAndExternalRef(t *testing.T) {
	var gotPath, gotQuery, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.RawQuery
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/pause"):
			_, _ = w.Write([]byte(`{"id":"sch_1","factory_id":"fac_1","automation":"audit","name":"weekly","cron":"@daily","paused":true}`))
		case strings.HasSuffix(r.URL.Path, "/resume"):
			_, _ = w.Write([]byte(`{"id":"sch_1","factory_id":"fac_1","automation":"audit","name":"weekly","cron":"@daily","paused":false}`))
		case r.URL.Path == "/api/v1/agent/schedules":
			_, _ = w.Write([]byte(`[{"id":"sch_1","factory_id":"fac_1","automation":"audit","cron":"@daily","paused":false}]`))
		default:
			_, _ = w.Write([]byte(`{"provider":"slack","external_ref":"slack:T/C/1","work_item_id":"wi_1","conversation_id":"conv_1","run":{"id":"run_1","status":"running"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}

	rows, err := c.ListSchedules(context.Background(), "fac 1")
	if err != nil || len(rows) != 1 || rows[0].Cron != "@daily" {
		t.Fatalf("list %+v err %v", rows, err)
	}
	if gotPath != "/api/v1/agent/schedules" || !strings.Contains(gotQuery, "factory=fac+1") {
		t.Fatalf("list %s %s?%s", gotMethod, gotPath, gotQuery)
	}

	paused, err := c.PauseSchedule(context.Background(), "sch/1")
	if err != nil || !paused.Paused || gotPath != "/api/v1/agent/schedules/sch%2F1/pause" {
		t.Fatalf("pause %+v path %s err %v", paused, gotPath, err)
	}
	resumed, err := c.ResumeSchedule(context.Background(), "sch_1")
	if err != nil || resumed.Paused || gotPath != "/api/v1/agent/schedules/sch_1/resume" {
		t.Fatalf("resume %+v path %s err %v", resumed, gotPath, err)
	}

	got, err := c.RunByExternalReference(context.Background(), "slack", "slack:T/C/1")
	if err != nil || got.WorkItemID != "wi_1" || got.Run == nil || got.Run.ID != "run_1" {
		t.Fatalf("%+v err %v", got, err)
	}
	if !strings.Contains(gotQuery, "provider=slack") || !strings.Contains(gotQuery, "ref=slack") {
		t.Fatalf("query %s", gotQuery)
	}
}

func TestAnswerInputSendsTextOnly(t *testing.T) {
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"inr_1","work_item_id":"wi_1","answer":"ship it","answered_by":"api-key"}`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	row, err := c.AnswerInput(context.Background(), "wi_1", "ship it")
	if err != nil {
		t.Fatal(err)
	}
	if row.AnsweredBy != "api-key" || gotPath != "/api/v1/work-items/wi_1/input" {
		t.Fatalf("%+v path %s", row, gotPath)
	}
	if string(gotBody) != `{"text":"ship it"}` {
		t.Fatalf("body %s", gotBody)
	}
	if _, err := c.AnswerInput(context.Background(), "wi_1", "  "); err == nil {
		t.Fatal("blank text was sent")
	}
}
