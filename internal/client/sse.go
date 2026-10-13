package client

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// Event is one Server-Sent Event. ID is the event's id field, empty when the
// block did not carry one. Data is the data field with the SSE trailing
// newline removed; multi-line data fields are joined with newlines.
type Event struct {
	ID   string
	Type string
	Data string
}

// ParseSSE reads an event stream until EOF. Comment lines and blocks with no
// data are discarded.
func ParseSSE(r io.Reader) ([]Event, error) {
	s := newSSEReader(r)
	var out []Event
	for {
		ev, err := s.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
}

type sseReader struct {
	r *bufio.Reader
}

func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{r: bufio.NewReader(r)}
}

func (s *sseReader) Next() (Event, error) {
	var (
		eventType string
		data      strings.Builder
		id        string
		hasID     bool
	)
	for {
		line, err := s.r.ReadString('\n')
		if len(line) == 0 {
			if err == nil {
				err = io.EOF
			}
			return Event{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if data.Len() == 0 {
				eventType = ""
				id = ""
				hasID = false
				if err != nil {
					return Event{}, err
				}
				continue
			}
			ev := Event{
				Type: eventType,
				Data: strings.TrimSuffix(data.String(), "\n"),
			}
			if hasID {
				ev.ID = id
			}
			return ev, nil
		}
		if err != nil {
			// A field without a terminating blank line is an incomplete event.
			return Event{}, err
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventType = value
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				id = value
				hasID = true
			}
		}
	}
}
