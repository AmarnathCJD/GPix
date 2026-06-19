package bridge

import "sync"

type queuedItem struct {
	ChatID    int64
	MessageID int32
	Name      string
}

type uploadQueue struct {
	mu    sync.Mutex
	items []queuedItem
}

func (q *uploadQueue) Add(it queuedItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.items {
		if e.ChatID == it.ChatID && e.MessageID == it.MessageID {
			return
		}
	}
	q.items = append(q.items, it)
}

func (q *uploadQueue) DrainN(n int) []queuedItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	if n <= 0 || n >= len(q.items) {
		out := q.items
		q.items = nil
		return out
	}
	out := q.items[:n]
	q.items = append([]queuedItem(nil), q.items[n:]...)
	return out
}

func (q *uploadQueue) Clear() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.items)
	q.items = nil
	return n
}

func (q *uploadQueue) Snapshot() []queuedItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]queuedItem, len(q.items))
	copy(out, q.items)
	return out
}

func (q *uploadQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}
