package dnscache

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

type Config struct {
	CacheTTL  string            `yaml:"cache_ttl"` // e.g. "30s"
	Servers   []string          `yaml:"servers"`   // e.g. ["8.8.8.8:53"]
	Overrides map[string]string `yaml:"overrides"` // host → IP
}

type entry struct {
	addrs   []string
	expires time.Time
}

type Resolver struct {
	mu        sync.RWMutex
	cache     map[string]*entry
	ttl       time.Duration
	overrides map[string]string
	dialer    *net.Resolver
}

func New(cfg Config) (*Resolver, error) {
	ttl := 30 * time.Second
	if cfg.CacheTTL != "" {
		d, err := time.ParseDuration(cfg.CacheTTL)
		if err != nil {
			return nil, fmt.Errorf("dns cache_ttl invalid: %w", err)
		}
		ttl = d
	}

	r := &Resolver{
		cache:     make(map[string]*entry),
		ttl:       ttl,
		overrides: cfg.Overrides,
	}

	if len(cfg.Servers) > 0 {
		servers := cfg.Servers
		r.dialer = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 5 * time.Second}

				server := servers[0]
				return d.DialContext(ctx, "udp", server)
			},
		}
	}

	return r, nil
}

func Default() *Resolver {
	r, _ := New(Config{})
	return r
}

func (r *Resolver) LookupHost(ctx context.Context, host string) ([]string, error) {

	if r.overrides != nil {
		if ip, ok := r.overrides[host]; ok {
			return []string{ip}, nil
		}
	}

	r.mu.RLock()
	e, ok := r.cache[host]
	r.mu.RUnlock()

	if ok && time.Now().Before(e.expires) {
		return e.addrs, nil
	}

	resolver := net.DefaultResolver
	if r.dialer != nil {
		resolver = r.dialer
	}

	addrs, err := resolver.LookupHost(ctx, host)
	if err != nil {
		if ok {
			return e.addrs, nil
		}
		return nil, fmt.Errorf("dns lookup %q: %w", host, err)
	}

	r.mu.Lock()
	r.cache[host] = &entry{
		addrs:   addrs,
		expires: time.Now().Add(r.ttl),
	}
	r.mu.Unlock()

	return addrs, nil
}

func (r *Resolver) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}

	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, ip := range addrs {
		conn, err := (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (r *Resolver) Flush() {
	r.mu.Lock()
	r.cache = make(map[string]*entry)
	r.mu.Unlock()
}

func (r *Resolver) FlushHost(host string) {
	r.mu.Lock()
	delete(r.cache, host)
	r.mu.Unlock()
}

func (r *Resolver) CacheSize() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.cache)
}
