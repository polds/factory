package linear

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var (
	errTruncated = errors.New("linear delivery body is truncated")
	errEmptyBody = errors.New("linear delivery body is empty")
)

// Normalize maps one verified Linear webhook body to zero or more events.
// See the package comment for which payloads emit which events.
func Normalize(d Delivery) ([]Event, error) {
	if d.Provider != "" && d.Provider != Provider {
		return nil, fmt.Errorf("linear normalizer: provider %q", d.Provider)
	}
	if d.Truncated {
		return nil, errTruncated
	}
	body := bytes.TrimSpace(d.Body)
	if len(body) == 0 {
		return nil, errEmptyBody
	}
	if bytes.Equal(body, []byte("null")) {
		return nil, errors.New("linear delivery body is null")
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("linear delivery body: %w", err)
	}
	switch env.Type {
	case "Issue":
		return fromIssue(d, body, env)
	case "Comment":
		return fromComment(d, body, env)
	case "AgentSessionEvent":
		return fromAgentSession(d, body, env)
	default:
		return nil, nil
	}
}

type envelope struct {
	Action        string          `json:"action"`
	Type          string          `json:"type"`
	Actor         *user           `json:"actor"`
	CreatedAt     string          `json:"createdAt"`
	Data          json.RawMessage `json:"data"`
	URL           string          `json:"url"`
	UpdatedFrom   json.RawMessage `json:"updatedFrom"`
	PromptContext string          `json:"promptContext"`
	AgentSession  json.RawMessage `json:"agentSession"`
}

type user struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type issueData struct {
	ID          string   `json:"id"`
	Number      float64  `json:"number"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	Identifier  string   `json:"identifier"`
	AssigneeID  *string  `json:"assigneeId"`
	LabelIDs    []string `json:"labelIds"`
	Labels      []label  `json:"labels"`
	StateID     string   `json:"stateId"`
}

type label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type commentData struct {
	Body    string      `json:"body"`
	IssueID string      `json:"issueId"`
	Issue   *issueChild `json:"issue"`
	User    *user       `json:"user"`
}

type issueChild struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

type agentSession struct {
	IssueID string        `json:"issueId"`
	Creator *user         `json:"creator"`
	Issue   *sessionIssue `json:"issue"`
	URL     string        `json:"url"`
}

type sessionIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

func fromIssue(d Delivery, body []byte, env envelope) ([]Event, error) {
	if env.Action != "create" && env.Action != "update" {
		return nil, nil
	}
	if len(bytes.TrimSpace(env.Data)) == 0 || bytes.Equal(bytes.TrimSpace(env.Data), []byte("null")) {
		return nil, errors.New("linear issue data is empty")
	}
	var data issueData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, fmt.Errorf("linear issue data: %w", err)
	}
	if data.ID == "" {
		return nil, errors.New("linear issue id is empty")
	}

	var names []string
	if env.Action == "create" {
		names = []string{EventIssueCreated}
	} else {
		updated, err := updatedKeys(env.UpdatedFrom)
		if err != nil {
			return nil, err
		}
		assigned, err := assigneeChanged(updated, data.AssigneeID)
		if err != nil {
			return nil, err
		}
		labeled, err := labelsChanged(updated, data.LabelIDs)
		if err != nil {
			return nil, err
		}
		state, err := stateChanged(updated, data.StateID)
		if err != nil {
			return nil, err
		}
		if assigned {
			names = append(names, EventIssueAssigned)
		}
		if labeled {
			names = append(names, EventIssueLabeled)
		}
		if state {
			names = append(names, EventIssueStateChanged)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	occurred, err := occurredAt(env.CreatedAt)
	if err != nil {
		return nil, err
	}
	number, err := issueNumber(data.Number, data.Identifier)
	if err != nil {
		return nil, err
	}
	subject := Subject{
		Kind:   "issue",
		ID:     data.ID,
		Number: number,
		Title:  data.Title,
		URL:    firstNonEmpty(data.URL, env.URL),
		Labels: labelNames(data.Labels),
		Text:   data.Description,
	}
	return build(d, body, names, occurred, actorFrom(env.Actor), subject, data.ID), nil
}

func fromComment(d Delivery, body []byte, env envelope) ([]Event, error) {
	if env.Action != "create" {
		return nil, nil
	}
	if len(bytes.TrimSpace(env.Data)) == 0 || bytes.Equal(bytes.TrimSpace(env.Data), []byte("null")) {
		return nil, errors.New("linear comment data is empty")
	}
	var data commentData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, fmt.Errorf("linear comment data: %w", err)
	}
	issueID := data.IssueID
	var child issueChild
	if data.Issue != nil {
		child = *data.Issue
		if child.ID != "" {
			issueID = child.ID
		}
	}
	if issueID == "" {
		return nil, nil
	}
	occurred, err := occurredAt(env.CreatedAt)
	if err != nil {
		return nil, err
	}
	number, err := issueNumber(0, child.Identifier)
	if err != nil {
		return nil, err
	}
	who := env.Actor
	if who == nil {
		who = data.User
	}
	subject := Subject{
		Kind:   "issue",
		ID:     issueID,
		Number: number,
		Title:  child.Title,
		URL:    child.URL,
		Labels: []string{},
		Text:   data.Body,
	}
	return build(d, body, []string{EventCommentCreated}, occurred, actorFrom(who), subject, issueID), nil
}

func fromAgentSession(d Delivery, body []byte, env envelope) ([]Event, error) {
	if env.Action != "created" {
		return nil, nil
	}
	if len(bytes.TrimSpace(env.AgentSession)) == 0 || bytes.Equal(bytes.TrimSpace(env.AgentSession), []byte("null")) {
		return nil, errors.New("linear agent session is empty")
	}
	var session agentSession
	if err := json.Unmarshal(env.AgentSession, &session); err != nil {
		return nil, fmt.Errorf("linear agent session: %w", err)
	}
	issueID := session.IssueID
	var issue sessionIssue
	if session.Issue != nil {
		issue = *session.Issue
		if issue.ID != "" {
			issueID = issue.ID
		}
	}
	if issueID == "" {
		return nil, nil
	}
	occurred, err := occurredAt(env.CreatedAt)
	if err != nil {
		return nil, err
	}
	number, err := issueNumber(0, issue.Identifier)
	if err != nil {
		return nil, err
	}
	text := env.PromptContext
	if text == "" {
		text = issue.Description
	}
	url := issue.URL
	if url == "" {
		url = session.URL
	}
	who := env.Actor
	if who == nil {
		who = session.Creator
	}
	subject := Subject{
		Kind:   "issue",
		ID:     issueID,
		Number: number,
		Title:  issue.Title,
		URL:    url,
		Labels: []string{},
		Text:   text,
	}
	return build(d, body, []string{EventAgentSessionCreated}, occurred, actorFrom(who), subject, issueID), nil
}

func build(d Delivery, body []byte, names []string, occurred time.Time, actor Actor, subject Subject, issueID string) []Event {
	if subject.Labels == nil {
		subject.Labels = []string{}
	}
	out := make([]Event, 0, len(names))
	for _, name := range names {
		out = append(out, Event{
			Provider:   Provider,
			Event:      name,
			DeliveryID: deliveryID(d),
			OccurredAt: occurred,
			Actor:      actor,
			Subject:    subject,
			Thread: Thread{
				Kind:        ThreadKind,
				ExternalRef: ExternalRef(issueID),
			},
			Raw: bytes.Clone(body),
		})
	}
	return out
}

func actorFrom(u *user) Actor {
	if u == nil {
		return Actor{}
	}
	return Actor{ProviderID: u.ID, Handle: u.Name}
}

func labelNames(labels []label) []string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names
}

func occurredAt(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("linear createdAt is empty")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("linear createdAt: %w", err)
	}
	return parsed, nil
}

func issueNumber(number float64, identifier string) (int, error) {
	if number == 0 {
		return identifierNumber(identifier), nil
	}
	// Issue numbers are small integers. Reject values float64 cannot round-trip.
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < 0 || number > 1<<31 {
		return 0, fmt.Errorf("linear issue number %v is not a non-negative integer", number)
	}
	return int(number), nil
}

func identifierNumber(identifier string) int {
	dash := strings.LastIndex(identifier, "-")
	if dash < 0 || dash == len(identifier)-1 {
		return 0
	}
	n, err := strconv.Atoi(identifier[dash+1:])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func deliveryID(d Delivery) string {
	if d.DeliveryID != "" {
		return d.DeliveryID
	}
	for key, value := range d.Headers {
		if strings.EqualFold(key, "Linear-Delivery") {
			return value
		}
	}
	return ""
}

func updatedKeys(raw json.RawMessage) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &keys); err != nil {
		return nil, fmt.Errorf("linear updatedFrom: %w", err)
	}
	return keys, nil
}

func assigneeChanged(updated map[string]json.RawMessage, current *string) (bool, error) {
	raw, ok := updated["assigneeId"]
	if !ok {
		return false, nil
	}
	prev, hasPrev, err := rawString(raw)
	if err != nil {
		return false, fmt.Errorf("linear updatedFrom.assigneeId: %w", err)
	}
	cur := ""
	hasCur := false
	if current != nil {
		cur = *current
		hasCur = true
	}
	return hasPrev != hasCur || prev != cur, nil
}

func stateChanged(updated map[string]json.RawMessage, current string) (bool, error) {
	raw, ok := updated["stateId"]
	if !ok {
		return false, nil
	}
	prev, hasPrev, err := rawString(raw)
	if err != nil {
		return false, fmt.Errorf("linear updatedFrom.stateId: %w", err)
	}
	if !hasPrev {
		return current != "", nil
	}
	return prev != current, nil
}

func labelsChanged(updated map[string]json.RawMessage, current []string) (bool, error) {
	raw, hasIDs := updated["labelIds"]
	_, hasLabels := updated["labels"]
	if !hasIDs && !hasLabels {
		return false, nil
	}
	if !hasIDs {
		return true, nil
	}
	prev, err := rawStrings(raw)
	if err != nil {
		return false, fmt.Errorf("linear updatedFrom.labelIds: %w", err)
	}
	if !sameIDs(prev, current) {
		return true, nil
	}
	return hasLabels, nil
}

func rawString(raw json.RawMessage) (string, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", false, err
	}
	return value, true, nil
}

func rawStrings(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal(trimmed, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := make(map[string]int, len(a))
	for _, id := range a {
		count[id]++
	}
	for _, id := range b {
		count[id]--
		if count[id] < 0 {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
