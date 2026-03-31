package session

import "sync"

// Store persists the topic filter list per client. It is used to restore
// subscriptions when a client reconnects with CleanSession=false.
type Store struct {
	mu   sync.RWMutex
	data map[string][]string
}

func New() *Store {
	return &Store{
		data: make(map[string][]string),
	}
}

// Save persists the filter list for the given client, replacing any previous
// entry. If filters is empty, the session is deleted instead.
func (s *Store) Save(clientID string, filters []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(filters) == 0 {
		delete(s.data, clientID)
		return
	}

	cp := make([]string, len(filters))
	copy(cp, filters)
	s.data[clientID] = cp
}

// Load returns the persisted filter list for the given client.
// The second return value is false if no session exists.
func (s *Store) Load(clientID string) ([]string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filters, ok := s.data[clientID]
	if !ok {
		return nil, false
	}

	cp := make([]string, len(filters))
	copy(cp, filters)
	return cp, true
}

// Delete removes the persisted session for the given client.
func (s *Store) Delete(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, clientID)
}
