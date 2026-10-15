package ingress

import (
	"bytes"
	"context"
	"errors"
	"sync"
)

const statusClaimed = "claimed"

type memStore struct {
	mu       sync.Mutex
	byKey    map[string]string
	all      map[string]Delivery
	status   map[string]string
	queue    []string
	enqueues int
	fail     error
}

func newMemStore() *memStore {
	return &memStore{
		byKey:  map[string]string{},
		all:    map[string]Delivery{},
		status: map[string]string{},
	}
}

func (m *memStore) Enqueue(ctx context.Context, d Delivery) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enqueues++
	if m.fail != nil {
		return false, m.fail
	}
	key := d.Provider + "\x00" + d.WebhookUID + "\x00" + d.DeliveryID
	if _, ok := m.byKey[key]; ok {
		return true, nil
	}
	m.byKey[key] = d.ID
	m.all[d.ID] = cloneDelivery(d)
	m.status[d.ID] = StatusReceived
	m.queue = append(m.queue, d.ID)
	return false, nil
}

func (m *memStore) ClaimReceived(ctx context.Context, limit int) ([]Delivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Delivery
	var rest []string
	for _, id := range m.queue {
		if len(out) < limit && m.status[id] == StatusReceived {
			m.status[id] = statusClaimed
			out = append(out, cloneDelivery(m.all[id]))
			continue
		}
		rest = append(rest, id)
	}
	m.queue = rest
	return out, nil
}

func (m *memStore) MarkDelivery(ctx context.Context, id, status string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.all[id]; !ok {
		return errors.New("ingress: unknown delivery")
	}
	m.status[id] = status
	if status == StatusReceived {
		m.queue = append(m.queue, id)
	}
	return nil
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.all)
}

func (m *memStore) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enqueues
}

func (m *memStore) one() Delivery {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.all {
		return cloneDelivery(d)
	}
	return Delivery{}
}

func (m *memStore) statusOf(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status[id]
}

func cloneDelivery(d Delivery) Delivery {
	d.Body = bytes.Clone(d.Body)
	if d.Headers != nil {
		h := make(map[string]string, len(d.Headers))
		for k, v := range d.Headers {
			h[k] = v
		}
		d.Headers = h
	}
	return d
}
