package comfy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fakeComfy(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/system_stats" {
			w.Write([]byte(`{"system":{"comfyui_version":"test"},"devices":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
}

func TestPoolLeaseAndAffinity(t *testing.T) {
	a, b := fakeComfy(t), fakeComfy(t)
	defer a.Close()
	defer b.Close()
	p := NewPool([]string{a.URL, b.URL})

	ctx := context.Background()
	// Lease both nodes, marking distinct warm models on release.
	c1, _, rel1, err := p.Acquire(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	c2, _, rel2, err := p.Acquire(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if c1.BaseURL == c2.BaseURL {
		t.Fatal("both leases landed on one node")
	}
	rel1("fl2va")
	rel2("ref2va")

	// Affinity: asking for ref2va must land on the node that has it warm.
	c3, _, rel3, err := p.Acquire(ctx, "ref2va")
	if err != nil {
		t.Fatal(err)
	}
	if c3.BaseURL != c2.BaseURL {
		t.Errorf("affinity miss: got %s, want %s (ref2va warm)", c3.BaseURL, c2.BaseURL)
	}
	rel3("ref2va")

	// A third concurrent lease blocks until a release frees a node.
	c4, _, rel4, _ := p.Acquire(ctx, "")
	c5, _, rel5, _ := p.Acquire(ctx, "")
	done := make(chan string, 1)
	go func() {
		c6, _, rel6, err := p.Acquire(ctx, "")
		if err != nil {
			done <- "err"
			return
		}
		rel6("")
		done <- c6.BaseURL
	}()
	select {
	case <-done:
		t.Fatal("third lease succeeded while both nodes were busy")
	case <-time.After(300 * time.Millisecond):
	}
	rel4("")
	select {
	case got := <-done:
		if got != c4.BaseURL {
			t.Errorf("released node not reused: got %s", got)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("blocked lease never woke after release")
	}
	rel5("")
	_ = c5
}

func TestPoolSkipsDownNode(t *testing.T) {
	a := fakeComfy(t)
	defer a.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer dead.Close()
	p := NewPool([]string{dead.URL, a.URL})

	c, _, rel, err := p.Acquire(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != a.URL {
		t.Errorf("lease landed on dead node %s", c.BaseURL)
	}
	rel("")
}
