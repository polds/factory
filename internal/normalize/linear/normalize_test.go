package linear

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	fixtureDeliveryID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	fixtureIssueID    = "11111111-1111-4111-8111-111111111111"
)

var claimedEvents = []string{
	EventIssueCreated,
	EventIssueAssigned,
	EventIssueLabeled,
	EventIssueStateChanged,
	EventCommentCreated,
	EventAgentSessionCreated,
}

func TestGolden(t *testing.T) {
	for _, name := range claimedEvents {
		t.Run(name, func(t *testing.T) {
			body := compactFile(t, filepath.Join("testdata", name, "input.json"))
			got, err := Normalize(Delivery{
				Provider:   Provider,
				DeliveryID: fixtureDeliveryID,
				Body:       body,
			})
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("events = %d, want 1", len(got))
			}
			ev := got[0]
			if ev.Provider != Provider || ev.Event != name {
				t.Fatalf("event = %s/%s", ev.Provider, ev.Event)
			}
			if ev.DeliveryID != fixtureDeliveryID {
				t.Fatalf("delivery_id = %s", ev.DeliveryID)
			}
			if ev.Thread.Kind != ThreadKind || ev.Thread.ExternalRef != ExternalRef(fixtureIssueID) {
				t.Fatalf("thread = %#v", ev.Thread)
			}
			if ev.Subject.Kind != "issue" || ev.Subject.ID != fixtureIssueID {
				t.Fatalf("subject = %#v", ev.Subject)
			}
			if ev.Actor.FactoryUserID != nil {
				t.Fatal("factory user id was set")
			}
			if !bytes.Equal(ev.Raw, body) {
				t.Fatal("raw bytes differ from the body")
			}
			gotJSON := mustIndent(t, got)
			path := filepath.Join("testdata", name, "event.json")
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotJSON, want) {
				t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", gotJSON, want)
			}
			var decoded []Event
			if err := json.Unmarshal(want, &decoded); err != nil {
				t.Fatal(err)
			}
			// MarshalIndent expands raw. Compare the JSON value, not those bytes.
			if !reflect.DeepEqual(compactRaw(t, decoded), compactRaw(t, got)) {
				t.Fatalf("round trip mismatch for %s", name)
			}
		})
	}
}

func TestGoldenDirsMatchClaimedEvents(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			got = append(got, entry.Name())
		}
	}
	want := append([]string(nil), claimedEvents...)
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dirs = %v, claimed = %v", got, want)
	}
}

func TestLabeledSubjectIsCurrentNames(t *testing.T) {
	got := normalizeFixture(t, EventIssueLabeled)
	want := []string{"bug", "factory-ready"}
	if !reflect.DeepEqual(got[0].Subject.Labels, want) {
		t.Fatalf("labels = %#v", got[0].Subject.Labels)
	}
}

func TestIssueCreatedDoesNotFanOut(t *testing.T) {
	got := normalizeFixture(t, EventIssueCreated)
	if len(got) != 1 || got[0].Event != EventIssueCreated {
		t.Fatalf("events = %#v", eventNames(got))
	}
	if got[0].Subject.Number != 123 || got[0].Subject.Title == "" {
		t.Fatalf("subject = %#v", got[0].Subject)
	}
	if len(got[0].Subject.Labels) != 1 || got[0].Subject.Labels[0] != "bug" {
		t.Fatalf("labels = %#v", got[0].Subject.Labels)
	}
}

func TestIssueUpdateRules(t *testing.T) {
	t.Run("title only", func(t *testing.T) {
		body := issueUpdate(t, func(env, data map[string]any) {
			data["title"] = "Renamed"
			env["updatedFrom"] = map[string]any{
				"title":     "Fix accessibility on checkout page",
				"updatedAt": "2026-01-08T16:00:00.084Z",
			}
		})
		assertNoEvents(t, body)
	})

	t.Run("updatedAt only", func(t *testing.T) {
		body := issueUpdate(t, func(env, _ map[string]any) {
			env["updatedFrom"] = map[string]any{"updatedAt": "2026-01-08T16:00:00.084Z"}
		})
		assertNoEvents(t, body)
	})

	t.Run("unassign", func(t *testing.T) {
		body := issueUpdate(t, func(env, data map[string]any) {
			data["assigneeId"] = nil
			delete(data, "assignee")
			env["updatedFrom"] = map[string]any{
				"assigneeId": "33333333-3333-4333-8333-333333333333",
			}
		})
		assertEventNames(t, body, EventIssueAssigned)
	})

	t.Run("same assignee", func(t *testing.T) {
		body := issueUpdate(t, func(env, _ map[string]any) {
			env["updatedFrom"] = map[string]any{
				"assigneeId": "33333333-3333-4333-8333-333333333333",
			}
		})
		assertNoEvents(t, body)
	})

	t.Run("label order only", func(t *testing.T) {
		body := issueUpdate(t, func(env, data map[string]any) {
			data["labelIds"] = []any{
				"55555555-5555-4555-8555-555555555555",
				"44444444-4444-4444-8444-444444444444",
			}
			env["updatedFrom"] = map[string]any{
				"labelIds": []any{
					"44444444-4444-4444-8444-444444444444",
					"55555555-5555-4555-8555-555555555555",
				},
			}
		})
		assertNoEvents(t, body)
	})

	t.Run("label removal", func(t *testing.T) {
		body := issueUpdate(t, func(env, data map[string]any) {
			data["labelIds"] = []any{"44444444-4444-4444-8444-444444444444"}
			data["labels"] = []any{map[string]any{
				"id":   "44444444-4444-4444-8444-444444444444",
				"name": "bug",
			}}
			env["updatedFrom"] = map[string]any{
				"labelIds": []any{
					"44444444-4444-4444-8444-444444444444",
					"55555555-5555-4555-8555-555555555555",
				},
			}
		})
		got := assertEventNames(t, body, EventIssueLabeled)
		if !reflect.DeepEqual(got[0].Subject.Labels, []string{"bug"}) {
			t.Fatalf("labels = %#v", got[0].Subject.Labels)
		}
	})

	t.Run("same label ids with labels object", func(t *testing.T) {
		body := issueUpdate(t, func(env, _ map[string]any) {
			env["updatedFrom"] = map[string]any{
				"labelIds": []any{"44444444-4444-4444-8444-444444444444"},
				"labels":   []any{map[string]any{"id": "44444444-4444-4444-8444-444444444444", "name": "defect"}},
			}
		})
		assertEventNames(t, body, EventIssueLabeled)
	})

	t.Run("same state", func(t *testing.T) {
		body := issueUpdate(t, func(env, _ map[string]any) {
			env["updatedFrom"] = map[string]any{"stateId": "66666666-6666-4666-8666-666666666666"}
		})
		assertNoEvents(t, body)
	})

	t.Run("assignee labels and state", func(t *testing.T) {
		body := issueUpdate(t, func(env, data map[string]any) {
			data["assigneeId"] = "33333333-3333-4333-8333-333333333333"
			data["stateId"] = "77777777-7777-4777-8777-777777777777"
			data["labelIds"] = []any{
				"44444444-4444-4444-8444-444444444444",
				"55555555-5555-4555-8555-555555555555",
			}
			env["updatedFrom"] = map[string]any{
				"assigneeId": nil,
				"labelIds":   []any{"44444444-4444-4444-8444-444444444444"},
				"stateId":    "66666666-6666-4666-8666-666666666666",
				"updatedAt":  "2026-01-08T16:00:00.084Z",
			}
		})
		got := assertEventNames(t, body, EventIssueAssigned, EventIssueLabeled, EventIssueStateChanged)
		for i, ev := range got {
			if ev.DeliveryID != fixtureDeliveryID || ev.Thread.ExternalRef != ExternalRef(fixtureIssueID) {
				t.Fatalf("event %d identity = %#v", i, ev)
			}
			if !bytes.Equal(ev.Raw, got[0].Raw) {
				t.Fatalf("event %d raw differs", i)
			}
		}
	})
}

func TestIgnoredPayloads(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "unknown type", body: `{"action":"create","type":"Project","createdAt":"not-a-time"}`},
		{name: "issue remove", body: `{"action":"remove","type":"Issue","data":{"id":"11111111-1111-4111-8111-111111111111"}}`},
		{name: "comment update", body: `{"action":"update","type":"Comment","data":{"issueId":"11111111-1111-4111-8111-111111111111","body":"edited"}}`},
		{name: "comment without issue", body: `{"action":"create","type":"Comment","createdAt":"2026-01-08T16:33:12.084Z","data":{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","body":"note"}}`},
		{name: "prompted session", body: `{"action":"prompted","type":"AgentSessionEvent","agentSession":{"issueId":"11111111-1111-4111-8111-111111111111"}}`},
		{name: "session without issue", body: `{"action":"created","type":"AgentSessionEvent","createdAt":"2026-01-08T16:33:12.084Z","agentSession":{"id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertNoEvents(t, []byte(tc.body))
		})
	}
}

func TestNormalizeErrors(t *testing.T) {
	valid := compactFile(t, filepath.Join("testdata", EventIssueCreated, "input.json"))
	t.Run("truncated", func(t *testing.T) {
		_, err := Normalize(Delivery{Provider: Provider, Body: valid, Truncated: true})
		if !errors.Is(err, errTruncated) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		_, err := Normalize(Delivery{Provider: Provider, Body: []byte(" \n")})
		if !errors.Is(err, errEmptyBody) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("null", func(t *testing.T) {
		_, err := Normalize(Delivery{Body: []byte("null")})
		if err == nil || !strings.Contains(err.Error(), "null") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		_, err := Normalize(Delivery{Body: []byte("{")})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("wrong provider", func(t *testing.T) {
		_, err := Normalize(Delivery{Provider: "github", Body: valid})
		if err == nil || !strings.Contains(err.Error(), "github") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad createdAt", func(t *testing.T) {
		body := issueUpdate(t, func(env, _ map[string]any) {
			env["action"] = "create"
			env["createdAt"] = "yesterday"
		})
		_, err := Normalize(Delivery{Provider: Provider, Body: body})
		if err == nil || !strings.Contains(err.Error(), "createdAt") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("updatedFrom array", func(t *testing.T) {
		body := issueUpdate(t, func(env, _ map[string]any) {
			env["updatedFrom"] = []any{"assigneeId"}
		})
		_, err := Normalize(Delivery{Provider: Provider, Body: body})
		if err == nil || !strings.Contains(err.Error(), "updatedFrom") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing issue id", func(t *testing.T) {
		body := issueUpdate(t, func(env, data map[string]any) {
			env["action"] = "create"
			delete(data, "id")
		})
		_, err := Normalize(Delivery{Provider: Provider, Body: body})
		if err == nil || !strings.Contains(err.Error(), "issue id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("fractional number", func(t *testing.T) {
		body := issueUpdate(t, func(env, data map[string]any) {
			env["action"] = "create"
			data["number"] = 1.5
		})
		_, err := Normalize(Delivery{Provider: Provider, Body: body})
		if err == nil || !strings.Contains(err.Error(), "number") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestActorIgnoresEmail(t *testing.T) {
	var env map[string]any
	if err := json.Unmarshal(compactFile(t, filepath.Join("testdata", EventIssueCreated, "input.json")), &env); err != nil {
		t.Fatal(err)
	}
	actor := env["actor"].(map[string]any)
	actor["email"] = "ada@example.test"
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Normalize(Delivery{Provider: Provider, DeliveryID: fixtureDeliveryID, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Actor.Handle != "Ada Lovelace" || got[0].Actor.ProviderID == "" {
		t.Fatalf("actor = %#v", got[0].Actor)
	}
	encoded, err := json.Marshal(got[0].Actor)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "ada@example.test") {
		t.Fatalf("actor json contains email: %s", encoded)
	}
	if !bytes.Contains(got[0].Raw, []byte("ada@example.test")) {
		t.Fatal("raw dropped the provider payload email")
	}
}

func TestNullActor(t *testing.T) {
	body := issueUpdate(t, func(env, _ map[string]any) {
		env["action"] = "create"
		env["actor"] = nil
	})
	got, err := Normalize(Delivery{Provider: Provider, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Actor.ProviderID != "" || got[0].Actor.Handle != "" {
		t.Fatalf("actor = %#v", got[0].Actor)
	}
}

func TestDeliveryIDHeader(t *testing.T) {
	body := compactFile(t, filepath.Join("testdata", EventCommentCreated, "input.json"))
	got, err := Normalize(Delivery{
		Provider: Provider,
		Headers:  map[string]string{"linear-delivery": fixtureDeliveryID},
		Body:     body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].DeliveryID != fixtureDeliveryID {
		t.Fatalf("delivery_id = %s", got[0].DeliveryID)
	}
	got, err = Normalize(Delivery{
		Provider:   Provider,
		DeliveryID: "explicit",
		Headers:    map[string]string{"Linear-Delivery": fixtureDeliveryID},
		Body:       body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].DeliveryID != "explicit" {
		t.Fatalf("explicit delivery id lost: %s", got[0].DeliveryID)
	}
}

func TestCommentUsesIssueURL(t *testing.T) {
	got := normalizeFixture(t, EventCommentCreated)
	if got[0].Subject.URL != "https://linear.app/acme/issue/ENG-123/fix-accessibility" {
		t.Fatalf("url = %s", got[0].Subject.URL)
	}
	if got[0].Subject.Text != "Please implement this" || got[0].Subject.Number != 123 {
		t.Fatalf("subject = %#v", got[0].Subject)
	}
	if len(got[0].Subject.Labels) != 0 {
		t.Fatalf("labels = %#v", got[0].Subject.Labels)
	}
}

func TestAgentSessionUsesPrompt(t *testing.T) {
	got := normalizeFixture(t, EventAgentSessionCreated)
	if got[0].Subject.Text != "Implement the checkout accessibility fix" {
		t.Fatalf("text = %q", got[0].Subject.Text)
	}
	if got[0].Actor.Handle != "Ada Lovelace" || got[0].Subject.Number != 123 {
		t.Fatalf("event = %#v", got[0])
	}
}

func TestRawKeepsInteriorWhitespace(t *testing.T) {
	body := []byte("{\"action\":\"create\",\"type\":\"Issue\",\"createdAt\":\"2026-01-08T16:33:12.084Z\",\"data\":{ \"id\":\"11111111-1111-4111-8111-111111111111\",\"title\":\"T\",\"number\":1 }}\n")
	got, err := Normalize(Delivery{Provider: Provider, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[0].Raw, bytes.TrimSpace(body)) {
		t.Fatalf("raw = %s", got[0].Raw)
	}
	if !bytes.Contains(got[0].Raw, []byte("{ \"id\"")) {
		t.Fatal("interior space was compacted")
	}
}

func TestNormalizeConcurrent(t *testing.T) {
	body := compactFile(t, filepath.Join("testdata", EventIssueLabeled, "input.json"))
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := Normalize(Delivery{Provider: Provider, DeliveryID: fixtureDeliveryID, Body: body})
			if err != nil {
				errCh <- err
				return
			}
			if len(got) != 1 || got[0].Event != EventIssueLabeled {
				errCh <- errors.New("unexpected events")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func normalizeFixture(t *testing.T, name string) []Event {
	t.Helper()
	body := compactFile(t, filepath.Join("testdata", name, "input.json"))
	got, err := Normalize(Delivery{Provider: Provider, DeliveryID: fixtureDeliveryID, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func issueUpdate(t *testing.T, mutate func(env, data map[string]any)) []byte {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(compactFile(t, filepath.Join("testdata", EventIssueCreated, "input.json")), &env); err != nil {
		t.Fatal(err)
	}
	env["action"] = "update"
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatal("fixture data is not an object")
	}
	mutate(env, data)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertNoEvents(t *testing.T, body []byte) {
	t.Helper()
	got, err := Normalize(Delivery{Provider: Provider, DeliveryID: fixtureDeliveryID, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("events = %v", eventNames(got))
	}
}

func assertEventNames(t *testing.T, body []byte, names ...string) []Event {
	t.Helper()
	got, err := Normalize(Delivery{Provider: Provider, DeliveryID: fixtureDeliveryID, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(eventNames(got), names) {
		t.Fatalf("events = %v, want %v", eventNames(got), names)
	}
	return got
}

func eventNames(events []Event) []string {
	names := make([]string, len(events))
	for i, ev := range events {
		names[i] = ev.Event
	}
	return names
}

func compactFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact %s: %v", path, err)
	}
	return buf.Bytes()
}

func compactRaw(t *testing.T, events []Event) []Event {
	t.Helper()
	out := make([]Event, len(events))
	for i, ev := range events {
		var buf bytes.Buffer
		if err := json.Compact(&buf, ev.Raw); err != nil {
			t.Fatal(err)
		}
		ev.Raw = buf.Bytes()
		out[i] = ev
	}
	return out
}

func mustIndent(t *testing.T, events []Event) []byte {
	t.Helper()
	buf, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(buf, '\n')
}
