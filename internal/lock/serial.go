package lock

import "sync"

type Serializable struct {
	mu sync.Mutex
}

func (s *Serializable) Lock() {
	s.mu.Lock()
}

func (s *Serializable) Unlock() {
	s.mu.Unlock()
}
