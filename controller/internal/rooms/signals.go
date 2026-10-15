package rooms

import (
	"strings"
	"sync"
)

type reconcileSignals struct {
	once  sync.Once
	mu    sync.Mutex
	ready chan struct{}
	hosts map[string]bool
	all   bool
}

func (s *reconcileSignals) channel() <-chan struct{} {
	s.once.Do(func() { s.ready = make(chan struct{}, 1); s.hosts = make(map[string]bool) })
	return s.ready
}

func (s *reconcileSignals) notify(host string) {
	s.channel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hosts) < 256 {
		s.hosts[strings.TrimSuffix(strings.ToLower(host), ".")] = true
	} else {
		s.all = true
	}
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

func (s *reconcileSignals) take() (map[string]bool, bool) {
	s.channel()
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.ready:
	default:
	}
	hosts, all := s.hosts, s.all
	s.hosts, s.all = make(map[string]bool), false
	return hosts, all
}

func (m *Manager) RequestReconcile(host string) { m.wake.notify(host) }
