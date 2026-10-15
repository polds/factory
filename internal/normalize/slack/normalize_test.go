package slack

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGoldenPairs(t *testing.T) {
	cases := []string{
		"app_mention",
		"message_posted",
		"message_posted_reply",
		"message_dm",
		"message_im",
		"message_mpim",
		"reaction_added",
		"member_joined_channel",
	}
	n := New("", "")
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			body := readFixture(t, name, "input.json")
			want := readFixture(t, name, "event.json")
			got, err := n.Normalize(deliveryFrom(t, body))
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("events = %d, want 1", len(got))
			}
			assertEventJSON(t, want, got[0])
		})
	}
}

func TestThreadReplyUsesRootExternalRef(t *testing.T) {
	n := New("", "")
	rootBody := readFixture(t, "message_posted", "input.json")
	replyBody := readFixture(t, "message_posted_reply", "input.json")
	root, err := n.Normalize(deliveryFrom(t, rootBody))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := n.Normalize(deliveryFrom(t, replyBody))
	if err != nil {
		t.Fatal(err)
	}
	if len(root) != 1 || len(reply) != 1 {
		t.Fatalf("root %d reply %d", len(root), len(reply))
	}
	if reply[0].Event != "message_posted" {
		t.Fatalf("reply event %q", reply[0].Event)
	}
	if reply[0].Thread.ExternalRef != root[0].Thread.ExternalRef {
		t.Fatalf("reply ref %s, root ref %s", reply[0].Thread.ExternalRef, root[0].Thread.ExternalRef)
	}
	if reply[0].Subject.ID == root[0].Subject.ID {
		t.Fatalf("reply subject id %s matches the root message", reply[0].Subject.ID)
	}
	const want = "slack:T0FACTORY/C0FACTORY/1600000000.000100"
	if root[0].Thread.ExternalRef != want {
		t.Fatalf("ref %s, want %s", root[0].Thread.ExternalRef, want)
	}
}

func TestDropsOwnMessagesAndEditJoinSubtypes(t *testing.T) {
	human := wrap("message", `{
		"type":"message","user":"U0HUMAN","text":"hello","ts":"1600000000.000100",
		"channel":"C0FACTORY","channel_type":"channel"
	}`)
	cases := []struct {
		name    string
		ownUser string
		botID   string
		body    string
	}{
		{
			name: "bot id without a user",
			body: wrap("message", `{
				"type":"message","subtype":"bot_message","bot_id":"B0BOT",
				"text":"status update","ts":"1600000000.000100",
				"channel":"C0FACTORY","channel_type":"channel"
			}`),
		},
		{
			name:  "configured bot id",
			botID: "B0BOT",
			body: wrap("message", `{
				"type":"message","bot_id":"B0BOT","user":"U0HUMAN",
				"text":"our post","ts":"1600000000.000100",
				"channel":"C0FACTORY","channel_type":"channel"
			}`),
		},
		{
			name:    "configured own user",
			ownUser: "U0ME",
			body: wrap("message", `{
				"type":"message","user":"U0ME","text":"our post","ts":"1600000000.000100",
				"channel":"C0FACTORY","channel_type":"channel"
			}`),
		},
		{
			name: "authorization bot user",
			body: wrap("message", `{
				"type":"message","user":"U0BOT","text":"our post","ts":"1600000000.000100",
				"channel":"C0FACTORY","channel_type":"channel"
			}`),
		},
		{
			name: "app mention from bot user",
			body: wrap("app_mention", `{
				"type":"app_mention","user":"U0BOT","text":"echo",
				"ts":"1600000000.000100","channel":"C0FACTORY"
			}`),
		},
		{
			name: "edit",
			body: wrap("message", `{
				"type":"message","subtype":"message_changed","hidden":true,"user":"U0HUMAN",
				"text":"edited","ts":"1600000000.000100","channel":"C0FACTORY","channel_type":"channel"
			}`),
		},
		{
			name: "delete",
			body: wrap("message", `{
				"type":"message","subtype":"message_deleted","hidden":true,
				"ts":"1600000000.000100","channel":"C0FACTORY","channel_type":"channel"
			}`),
		},
		{
			name: "channel join",
			body: wrap("message", `{
				"type":"message","subtype":"channel_join","user":"U0HUMAN",
				"text":"joined","ts":"1600000000.000100","channel":"C0FACTORY","channel_type":"channel"
			}`),
		},
		{
			name: "group join",
			body: wrap("message", `{
				"type":"message","subtype":"group_join","user":"U0HUMAN",
				"text":"joined","ts":"1600000000.000100","channel":"G0FACTORY","channel_type":"group"
			}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New(tc.ownUser, tc.botID).Normalize(Delivery{
				Provider:   "slack",
				DeliveryID: "EvDROP",
				Body:       []byte(tc.body),
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("events = %d, want 0 (%s)", len(got), got[0].Event)
			}
		})
	}
	t.Run("other bot kept", func(t *testing.T) {
		for _, botID := range []string{"", "B0BOT"} {
			got, err := New("", botID).Normalize(Delivery{
				Provider:   "slack",
				DeliveryID: "EvOTHER",
				Body:       []byte(humanWithBot("B0OTHER", "U0OTHER")),
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Event != "message_posted" || got[0].Actor.ProviderID != "U0OTHER" {
				t.Fatalf("botID %q got %+v", botID, got)
			}
		}
	})
	t.Run("human kept", func(t *testing.T) {
		got, err := New("U0BOT", "B0BOT").Normalize(Delivery{
			Provider:   "slack",
			DeliveryID: "EvHUMAN",
			Body:       []byte(human),
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Event != "message_posted" || got[0].Actor.ProviderID != "U0HUMAN" {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestMessageChannelTypes(t *testing.T) {
	cases := []struct {
		channelType string
		event       string
		keep        bool
	}{
		{channelType: "group", event: "message_posted", keep: true},
		{channelType: "", event: "message_posted", keep: true},
		{channelType: "dm", event: "message_dm", keep: true},
		{channelType: "file_share_channel", event: "", keep: false},
	}
	for _, tc := range cases {
		name := tc.channelType
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			ev := `{"type":"message","user":"U0HUMAN","text":"hello","ts":"1600000000.000100","channel":"C0FACTORY"`
			if tc.channelType != "" && tc.channelType != "file_share_channel" {
				ev += `,"channel_type":"` + tc.channelType + `"`
			}
			if tc.channelType == "file_share_channel" {
				ev += `,"channel_type":"mystery"`
			}
			ev += `}`
			got, err := New("", "").Normalize(Delivery{
				Provider:   "slack",
				DeliveryID: "EvCHAN",
				Body:       []byte(wrap("message", ev)),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.keep {
				if len(got) != 0 {
					t.Fatalf("events = %d, want 0", len(got))
				}
				return
			}
			if len(got) != 1 || got[0].Event != tc.event {
				t.Fatalf("got %+v, want %s", got, tc.event)
			}
		})
	}
}

func TestFileShareIsMessagePosted(t *testing.T) {
	body := wrap("message", `{
		"type":"message","subtype":"file_share","user":"U0HUMAN","text":"see the file",
		"ts":"1600000000.000100","channel":"C0FACTORY","channel_type":"channel"
	}`)
	got, err := New("", "").Normalize(Delivery{Provider: "slack", DeliveryID: "EvFILE", Body: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Event != "message_posted" || got[0].Subject.Text != "see the file" {
		t.Fatalf("got %+v", got)
	}
}

func TestAppMentionThreadUsesRootRef(t *testing.T) {
	body := wrap("app_mention", `{
		"type":"app_mention","user":"U0HUMAN","text":"<@U0BOT> ping",
		"ts":"1600000003.000100","thread_ts":"1600000000.000100","channel":"C0FACTORY"
	}`)
	got, err := New("", "").Normalize(Delivery{Provider: "slack", DeliveryID: "EvMENTION", Body: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatal(len(got))
	}
	if got[0].Event != "app_mention" {
		t.Fatalf("event %s", got[0].Event)
	}
	if got[0].Thread.Kind != "slack_thread" {
		t.Fatalf("kind %s", got[0].Thread.Kind)
	}
	if got[0].Thread.ExternalRef != "slack:T0FACTORY/C0FACTORY/1600000000.000100" {
		t.Fatalf("ref %s", got[0].Thread.ExternalRef)
	}
	if got[0].Subject.ID != "1600000003.000100" {
		t.Fatalf("subject %s", got[0].Subject.ID)
	}
}

func TestEnvelopeTokenIsNotCopied(t *testing.T) {
	const token = "xoxb-test-secret"
	body := `{
		"token":"` + token + `",
		"type":"event_callback",
		"team_id":"T0FACTORY",
		"event_id":"EvTOK",
		"event_time":1600000000,
		"authorizations":[{"user_id":"U0BOT","is_bot":true}],
		"event":{"type":"message","user":"U0HUMAN","text":"hello","ts":"1600000000.000100","channel":"C0FACTORY","channel_type":"channel"}
	}`
	got, err := New("", "").Normalize(Delivery{Provider: "slack", DeliveryID: "EvTOK", Body: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "xoxb-") {
		t.Fatalf("token leaked into events: %s", raw)
	}
}

func TestDeliveryIDPrefersDelivery(t *testing.T) {
	body := readFixture(t, "message_posted", "input.json")
	got, err := New("", "").Normalize(Delivery{Provider: "slack", DeliveryID: "dlv_explicit", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DeliveryID != "dlv_explicit" {
		t.Fatalf("got %+v", got)
	}
}

func TestOccurredAtFallsBackToMessageTimestamp(t *testing.T) {
	body := `{
		"type":"event_callback",
		"team_id":"T0FACTORY",
		"event_id":"EvTIME",
		"authorizations":[{"user_id":"U0BOT","is_bot":true}],
		"event":{"type":"message","user":"U0HUMAN","text":"hello","ts":"1600000000.000100","channel":"C0FACTORY","channel_type":"channel"}
	}`
	got, err := New("", "").Normalize(Delivery{Provider: "slack", DeliveryID: "EvTIME", Body: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2020, 9, 13, 12, 26, 40, 100000, time.UTC)
	if len(got) != 1 || !got[0].OccurredAt.Equal(want) {
		t.Fatalf("occurred_at %s, want %s", got[0].OccurredAt, want)
	}
}

func TestIgnorableEnvelopes(t *testing.T) {
	cases := map[string]string{
		"url_verification": `{"type":"url_verification","challenge":"abc","token":"xoxb-test-secret"}`,
		"app_rate_limited": `{"type":"app_rate_limited","team_id":"T0FACTORY"}`,
		"unknown_event": `{
			"type":"event_callback","team_id":"T0FACTORY","event_id":"EvUNK","event_time":1600000000,
			"event":{"type":"file_created","file_id":"F1"}
		}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := New("", "").Normalize(Delivery{Provider: "slack", DeliveryID: "EvIGN", Body: []byte(body)})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("events = %d", len(got))
			}
		})
	}
}

func TestNormalizeErrors(t *testing.T) {
	cases := map[string]Delivery{
		"empty":     {Provider: "slack", Body: nil},
		"truncated": {Provider: "slack", Body: []byte(`{"type":"event_callback"}`), Truncated: true},
		"json":      {Provider: "slack", Body: []byte(`{"type":`)},
		"missing event": {
			Provider: "slack",
			Body:     []byte(`{"type":"event_callback","team_id":"T0FACTORY","event_id":"EvX"}`),
		},
		"missing channel": {
			Provider: "slack",
			Body: []byte(wrap("message", `{
				"type":"message","user":"U0HUMAN","text":"hello","ts":"1600000000.000100","channel_type":"channel"
			}`)),
		},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := New("", "").Normalize(d)
			if err == nil {
				t.Fatalf("events %+v, want error", got)
			}
			if strings.Contains(err.Error(), "xoxb-") {
				t.Fatalf("error leaked a token: %v", err)
			}
		})
	}
}

func TestNormalizeConcurrent(t *testing.T) {
	d := deliveryFrom(t, readFixture(t, "message_posted", "input.json"))
	n := New("U0BOT", "B0BOT")
	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := n.Normalize(d)
			if err != nil {
				errCh <- err
				return
			}
			if len(got) != 1 || got[0].Event != "message_posted" {
				errCh <- errors.New("concurrent normalize returned an unexpected event")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func deliveryFrom(t *testing.T, body []byte) Delivery {
	t.Helper()
	var probe struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		t.Fatal(err)
	}
	return Delivery{Provider: "slack", DeliveryID: probe.EventID, Body: body}
}

func readFixture(t *testing.T, name, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name, file))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertEventJSON(t *testing.T, wantJSON []byte, got Event) {
	t.Helper()
	var want Event
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatal(err)
	}
	got.OccurredAt = got.OccurredAt.UTC().Round(0)
	want.OccurredAt = want.OccurredAt.UTC().Round(0)
	got.Raw = canonRaw(t, got.Raw)
	want.Raw = canonRaw(t, want.Raw)
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		t.Fatalf("event mismatch\n got %s\nwant %s", gb, wb)
	}
}

func canonRaw(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func wrap(eventType, event string) string {
	return `{
		"type":"event_callback",
		"team_id":"T0FACTORY",
		"event_id":"Ev` + eventType + `",
		"event_time":1600000000,
		"authorizations":[{"user_id":"U0BOT","is_bot":true,"team_id":"T0FACTORY"}],
		"event":` + event + `
	}`
}

func humanWithBot(botID, user string) string {
	return wrap("message", `{
		"type":"message","bot_id":"`+botID+`","user":"`+user+`",
		"text":"other bot","ts":"1600000000.000100",
		"channel":"C0FACTORY","channel_type":"channel"
	}`)
}
