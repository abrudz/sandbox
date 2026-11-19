package api

import (
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

// The API contract's argument limits.
const (
	maxArgs   = 16
	maxArgLen = 1024
)

// bodyLimit is the largest request body read. It allows code, input and
// arguments at their limits with every byte JSON-escaped (six bytes per
// byte) plus framing, so a body over it cannot be a valid request.
func (s *Server) bodyLimit() int64 {
	return 6*int64(s.opts.RequestLimit+maxArgs*maxArgLen) + 4096
}

// admission tracks runs in progress and recent run starts. A limit of zero
// in Options means no limit.
type admission struct {
	mu        sync.Mutex
	draining  bool
	drained   chan struct{} // closed once draining with no run in progress
	active    int
	perClient map[string]int
	starts    map[string][]time.Time // run starts in the last minute
	swept     time.Time
}

// admit reserves a run for client, or returns the status and reason for
// refusing it. release must be called once the run has finished.
func (s *Server) admit(client string) (release func(), status int, reason string) {
	a := &s.admission
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.draining {
		return nil, http.StatusServiceUnavailable, "the service is shutting down"
	}
	now := s.now()
	if s.opts.ClientRate > 0 {
		a.sweep(now)
		recent := since(a.starts[client], now.Add(-time.Minute))
		a.starts[client] = recent
		if len(recent) >= s.opts.ClientRate {
			return nil, http.StatusTooManyRequests, "this client has started its maximum number of runs in the last minute"
		}
	}
	if s.opts.ClientConcurrency > 0 && a.perClient[client] >= s.opts.ClientConcurrency {
		return nil, http.StatusTooManyRequests, "this client has its maximum number of runs in progress"
	}
	if s.opts.Concurrency > 0 && a.active >= s.opts.Concurrency {
		return nil, http.StatusTooManyRequests, "every run slot is busy"
	}

	a.active++
	a.perClient[client]++
	if s.opts.ClientRate > 0 {
		a.starts[client] = append(a.starts[client], now)
	}
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.active--
		if a.perClient[client]--; a.perClient[client] == 0 {
			delete(a.perClient, client)
		}
		if a.draining && a.active == 0 {
			close(a.drained)
		}
	}, 0, ""
}

// since returns the times after cutoff; times are in ascending order.
func since(times []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	return times[i:]
}

// sweep forgets clients with no run start in the last minute, at most once
// a minute, so the map does not grow with every client ever seen.
func (a *admission) sweep(now time.Time) {
	if now.Sub(a.swept) < time.Minute {
		return
	}
	a.swept = now
	for client, times := range a.starts {
		if len(since(times, now.Add(-time.Minute))) == 0 {
			delete(a.starts, client)
		}
	}
}

// clientOf identifies the client behind r. A trusted proxy appends the
// address it saw to X-Forwarded-For, so the client is the right-most entry
// that is not itself a trusted proxy; entries further left are the client's
// own claims. Any other peer is the client.
func (s *Server) clientOf(r *http.Request) string {
	peer := peerAddr(r)
	if !s.trusted(peer) {
		return peer.String()
	}
	entries := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for _, entry := range slices.Backward(entries) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return entry
		}
		if addr = addr.Unmap(); !s.trusted(addr) {
			return addr.String()
		}
	}
	return peer.String()
}

func peerAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

func (s *Server) trusted(addr netip.Addr) bool {
	for _, p := range s.opts.TrustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func (s *Server) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}
