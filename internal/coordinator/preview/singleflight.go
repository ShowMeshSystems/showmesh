package preview

import "sync"

type flight struct {
	done  chan struct{}
	value []byte
	err   error
}

// singleflight shares one in-progress snapshot call among callers asking for the same key.
type singleflight struct {
	mu      sync.Mutex
	flights map[string]*flight
}

func (g *singleflight) do(key string, fn func() ([]byte, error)) ([]byte, error) {
	g.mu.Lock()
	if f, ok := g.flights[key]; ok {
		g.mu.Unlock()
		<-f.done
		return f.value, f.err
	}
	if g.flights == nil {
		g.flights = map[string]*flight{}
	}
	f := &flight{done: make(chan struct{})}
	g.flights[key] = f
	g.mu.Unlock()

	f.value, f.err = fn()
	g.mu.Lock()
	delete(g.flights, key)
	g.mu.Unlock()
	close(f.done)
	return f.value, f.err
}
