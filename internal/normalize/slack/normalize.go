package slack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Normalizer maps one verified Slack delivery to canonical events.
// The zero value drops nothing by configured id; authorization bot users
// and anonymous bot_id posts are still dropped.
type Normalizer struct {
	ownUserID string
	botID     string
}

// New returns a normalizer. ownUserID is this app's bot user id.
// botID is this app's bot_id. Either string may be empty.
func New(ownUserID, botID string) Normalizer {
	return Normalizer{ownUserID: ownUserID, botID: botID}
}

// Normalize returns the canonical events for d. Zero events and a nil error
// means the delivery is ignorable. Malformed known events return an error.
func (n Normalizer) Normalize(d Delivery) ([]Event, error) {
	if d.Truncated {
		return nil, errors.New("slack: delivery body truncated")
	}
	body := bytes.TrimSpace(d.Body)
	if len(body) == 0 {
		return nil, errors.New("slack: empty body")
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("slack: parse delivery: %w", err)
	}
	if env.Type != "event_callback" {
		return nil, nil
	}
	raw := bytes.TrimSpace(env.Event)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, errors.New("slack: missing event")
	}
	var ev slackEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Errorf("slack: parse event: %w", err)
	}
	if n.own(env, ev) {
		return nil, nil
	}
	switch ev.Type {
	case "app_mention":
		return build(d, env, ev, raw, parts{
			name: "app_mention", kind: "message", text: ev.Text,
			channel: ev.Channel, subjectID: subjectStamp(ev), threadTS: threadStamp(ev),
		})
	case "message":
		if droppedSubtype(ev.Subtype) {
			return nil, nil
		}
		name, ok := messageEvent(ev.ChannelType)
		if !ok {
			return nil, nil
		}
		return build(d, env, ev, raw, parts{
			name: name, kind: "message", text: ev.Text,
			channel: ev.Channel, subjectID: subjectStamp(ev), threadTS: threadStamp(ev),
		})
	case "reaction_added":
		return build(d, env, ev, raw, parts{
			name: "reaction_added", kind: "reaction", text: ev.Reaction,
			channel: ev.Item.Channel, subjectID: ev.Item.TS, threadTS: ev.Item.TS,
		})
	case "member_joined_channel":
		stamp := subjectStamp(ev)
		return build(d, env, ev, raw, parts{
			name: "member_joined_channel", kind: "member",
			channel: ev.Channel, subjectID: stamp, threadTS: stamp,
		})
	default:
		return nil, nil
	}
}

// parts holds the fields that differ across Slack event types.
type parts struct {
	name      string
	kind      string
	text      string
	channel   string
	subjectID string
	threadTS  string
}

func build(d Delivery, env envelope, ev slackEvent, raw []byte, p parts) ([]Event, error) {
	team := env.TeamID
	if team == "" {
		team = ev.Team
	}
	if team == "" || p.channel == "" || p.threadTS == "" || p.subjectID == "" {
		return nil, errors.New("slack: incomplete thread ref")
	}
	occurred, err := occurredAt(env, ev)
	if err != nil {
		return nil, err
	}
	deliveryID := d.DeliveryID
	if deliveryID == "" {
		deliveryID = env.EventID
	}
	return []Event{{
		Provider:   "slack",
		Event:      p.name,
		DeliveryID: deliveryID,
		OccurredAt: occurred,
		Actor:      Actor{ProviderID: ev.User},
		Subject: Subject{
			Kind: p.kind,
			ID:   p.subjectID,
			Text: p.text,
		},
		Thread: Thread{
			Kind:        "slack_thread",
			ExternalRef: "slack:" + team + "/" + p.channel + "/" + p.threadTS,
		},
		Raw: append(json.RawMessage(nil), raw...),
	}}, nil
}

func (n Normalizer) own(env envelope, ev slackEvent) bool {
	if ev.BotID != "" && n.botID != "" && ev.BotID == n.botID {
		return true
	}
	if ev.BotID != "" && ev.User == "" {
		return true
	}
	if n.ownUserID != "" && ev.User != "" && ev.User == n.ownUserID {
		return true
	}
	for _, a := range env.Authorizations {
		if a.IsBot && a.UserID != "" && ev.User == a.UserID {
			return true
		}
	}
	return false
}

func droppedSubtype(subtype string) bool {
	switch subtype {
	case "message_changed", "message_deleted", "channel_join", "group_join":
		return true
	default:
		return false
	}
}

func messageEvent(channelType string) (string, bool) {
	switch channelType {
	case "channel", "group", "":
		return "message_posted", true
	case "im":
		return "message_im", true
	case "mpim":
		return "message_mpim", true
	case "app_home", "dm":
		return "message_dm", true
	default:
		return "", false
	}
}

func threadStamp(ev slackEvent) string {
	if ev.ThreadTS != "" {
		return ev.ThreadTS
	}
	return subjectStamp(ev)
}

func subjectStamp(ev slackEvent) string {
	if ev.TS != "" {
		return ev.TS
	}
	return ev.EventTS
}

func occurredAt(env envelope, ev slackEvent) (time.Time, error) {
	if env.EventTime > 0 {
		return time.Unix(int64(env.EventTime), 0).UTC(), nil
	}
	stamp := ev.EventTS
	if stamp == "" {
		stamp = ev.TS
	}
	if stamp == "" {
		stamp = ev.Item.TS
	}
	if stamp == "" {
		return time.Time{}, errors.New("slack: event has no time")
	}
	return parseSlackTS(stamp)
}

func parseSlackTS(ts string) (time.Time, error) {
	secStr, frac, hasFrac := strings.Cut(ts, ".")
	sec, err := strconv.ParseInt(secStr, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("slack: bad timestamp %q", ts)
	}
	var nsec int64
	if hasFrac {
		if len(frac) > 6 {
			frac = frac[:6]
		}
		for len(frac) < 6 {
			frac += "0"
		}
		micro, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("slack: bad timestamp %q", ts)
		}
		nsec = micro * int64(time.Microsecond)
	}
	return time.Unix(sec, nsec).UTC(), nil
}

type envelope struct {
	Type           string          `json:"type"`
	TeamID         string          `json:"team_id"`
	EventID        string          `json:"event_id"`
	EventTime      float64         `json:"event_time"`
	Authorizations []authorization `json:"authorizations"`
	Event          json.RawMessage `json:"event"`
}

type authorization struct {
	UserID string `json:"user_id"`
	IsBot  bool   `json:"is_bot"`
}

type slackEvent struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	User        string `json:"user"`
	BotID       string `json:"bot_id"`
	Text        string `json:"text"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	EventTS     string `json:"event_ts"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	Team        string `json:"team"`
	Reaction    string `json:"reaction"`
	Item        struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	} `json:"item"`
}
