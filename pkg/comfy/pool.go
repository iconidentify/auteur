package comfy

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"
)

// Pool schedules renders across several ComfyUI servers. A clip leases one
// node for its whole lifecycle — uploads, queue, polling, and download must
// all hit the same server — and releases it when done. Selection prefers a
// node whose last-loaded model matches the request (model swaps cost a 21GB
// reload), then any idle node. Nodes that fail a health probe sit out a
// cooldown instead of poisoning the shoot.
type Pool struct {
	mu    sync.Mutex
	nodes []*poolNode
	wake  chan struct{}
}

type poolNode struct {
	client    *Client
	name      string
	busy      bool
	lastModel string
	downUntil time.Time
}

const nodeCooldown = 45 * time.Second

// NewPool builds a pool over the given base URLs (order = preference order
// for ties). Names derive from each URL's host.
func NewPool(urls []string) *Pool {
	p := &Pool{wake: make(chan struct{}, 1)}
	for _, u := range urls {
		name := u
		if parsed, err := url.Parse(u); err == nil && parsed.Host != "" {
			name = parsed.Host
		}
		p.nodes = append(p.nodes, &poolNode{client: NewClient(u), name: name})
	}
	return p
}

// Size is the number of nodes; the natural clip concurrency.
func (p *Pool) Size() int { return len(p.nodes) }

// Nodes returns each node's name and URL for logging.
func (p *Pool) Nodes() []string {
	var out []string
	for _, n := range p.nodes {
		out = append(out, fmt.Sprintf("%s (%s)", n.name, n.client.BaseURL))
	}
	return out
}

// Ping health-checks every node and returns a per-node description; err is
// non-nil only when no node is reachable.
func (p *Pool) Ping(ctx context.Context) ([]string, error) {
	var out []string
	healthy := 0
	for _, n := range p.nodes {
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		desc, err := n.client.Ping(cctx)
		cancel()
		if err != nil {
			out = append(out, fmt.Sprintf("%s: UNREACHABLE (%v)", n.name, err))
			continue
		}
		healthy++
		out = append(out, fmt.Sprintf("%s: %s", n.name, desc))
	}
	if healthy == 0 {
		return out, fmt.Errorf("no render node reachable")
	}
	return out, nil
}

// Acquire leases a node, blocking until one is free and healthy (or ctx
// ends). wantModel biases selection toward a node with that model warm.
// The returned release must be called exactly once, with the model the
// render actually loaded.
func (p *Pool) Acquire(ctx context.Context, wantModel string) (client *Client, name string, release func(loadedModel string), err error) {
	for {
		if n := p.tryPick(ctx, wantModel); n != nil {
			rel := func(loaded string) {
				p.mu.Lock()
				n.busy = false
				if loaded != "" {
					n.lastModel = loaded
				}
				p.mu.Unlock()
				select {
				case p.wake <- struct{}{}:
				default:
				}
			}
			return n.client, n.name, rel, nil
		}
		select {
		case <-ctx.Done():
			return nil, "", nil, ctx.Err()
		case <-p.wake:
		case <-time.After(5 * time.Second):
		}
	}
}

// tryPick claims the best free node, health-probing candidates. Returns nil
// when nothing suitable is available right now.
func (p *Pool) tryPick(ctx context.Context, wantModel string) *poolNode {
	p.mu.Lock()
	var candidates []*poolNode
	now := time.Now()
	for _, n := range p.nodes {
		if !n.busy && now.After(n.downUntil) {
			candidates = append(candidates, n)
		}
	}
	// Warm-model affinity first, then never-used, then anything free.
	pick := func(match func(*poolNode) bool) *poolNode {
		for _, n := range candidates {
			if match(n) {
				return n
			}
		}
		return nil
	}
	ordered := []*poolNode{}
	if n := pick(func(n *poolNode) bool { return wantModel != "" && n.lastModel == wantModel }); n != nil {
		ordered = append(ordered, n)
	}
	if n := pick(func(n *poolNode) bool { return n.lastModel == "" }); n != nil {
		ordered = append(ordered, n)
	}
	ordered = append(ordered, candidates...)
	p.mu.Unlock()

	seen := map[*poolNode]bool{}
	for _, n := range ordered {
		if seen[n] {
			continue
		}
		seen[n] = true
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := n.client.Ping(cctx)
		cancel()
		p.mu.Lock()
		if err != nil {
			n.downUntil = time.Now().Add(nodeCooldown)
			p.mu.Unlock()
			continue
		}
		if n.busy { // raced with another clip
			p.mu.Unlock()
			continue
		}
		n.busy = true
		p.mu.Unlock()
		return n
	}
	return nil
}
