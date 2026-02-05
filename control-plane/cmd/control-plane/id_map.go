package main

import "sync"

type idMap struct {
	mu   sync.RWMutex
	data map[string]string
}

func newIDMap() *idMap {
	return &idMap{data: map[string]string{}}
}

func (m *idMap) Set(id, ns string) {
	m.mu.Lock()
	m.data[id] = ns
	m.mu.Unlock()
}

func (m *idMap) Resolve(id string) string {
	m.mu.RLock()
	ns, ok := m.data[id]
	m.mu.RUnlock()
	if ok && ns != "" {
		return ns
	}
	return id
}

func (m *idMap) Delete(id string) {
	m.mu.Lock()
	delete(m.data, id)
	m.mu.Unlock()
}
