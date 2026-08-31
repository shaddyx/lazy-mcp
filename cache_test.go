package main

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerCache_GetOrCreate_Dedup(t *testing.T) {
	cache := NewServerCache()
	def := &ServerDef{Command: "echo"}

	// First create.
	s1 := cache.GetOrCreate(def, "a.b")
	if s1 == nil {
		t.Fatal("GetOrCreate returned nil")
	}
	// Subsequent gets must return the same pointer.
	s2 := cache.GetOrCreate(def, "a.b")
	if s1 != s2 {
		t.Fatalf("expected same CachedServer pointer, got %p and %p", s1, s2)
	}
}

func TestServerCache_ConcurrentGetOrCreate(t *testing.T) {
	cache := NewServerCache()
	def := &ServerDef{Command: "echo"}
	var wg sync.WaitGroup
	const n = 50
	results := make([]*CachedServer, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = cache.GetOrCreate(def, "shared.path")
		}(i)
	}
	wg.Wait()
	first := results[0]
	for i, r := range results {
		if r != first {
			t.Fatalf("goroutine %d got different CachedServer pointer", i)
		}
	}
}

func TestCachedServer_LazyLoadOnce(t *testing.T) {
	ctx := context.Background()
	clientTransport, cleanup := newFakeDownstream(t, ctx)
	defer cleanup()

	connects := 0
	var connectMu sync.Mutex
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		connectMu.Lock()
		connects++
		connectMu.Unlock()
		return clientTransport, nil
	}
	cache := NewServerCacheWithFactory(factory)
	def := &ServerDef{Command: "echo"}
	cs := cache.GetOrCreate(def, "x.y")

	// Concurrent ensureConnected from many goroutines must connect exactly once.
	var wg sync.WaitGroup
	const n = 20
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = cs.ensureConnected(ctx)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: ensureConnected: %v", i, err)
		}
	}
	if connects != 1 {
		t.Errorf("expected 1 connection, got %d", connects)
	}
	if len(cs.Tools) != 1 || cs.Tools[0].Name != "echo" {
		t.Errorf("cached tools = %+v, want single echo", cs.Tools)
	}
	// Second ensureConnected (no new connection).
	if err := cs.ensureConnected(ctx); err != nil {
		t.Fatalf("second ensureConnected: %v", err)
	}
	if connects != 1 {
		t.Errorf("expected still 1 connection after re-call, got %d", connects)
	}
}

func TestFilterTools_AllowList(t *testing.T) {
	tools := []*mcp.Tool{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	if got := filterTools(tools, nil); len(got) != 3 {
		t.Errorf("nil allow: got %d", len(got))
	}
	if got := filterTools(tools, []string{}); len(got) != 3 {
		t.Errorf("empty allow: got %d", len(got))
	}
	got := filterTools(tools, []string{"a", "c"})
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Errorf("allow-list filter = %+v", got)
	}
}

func TestTransportFor(t *testing.T) {
	stdio, err := transportFor(&ServerDef{Command: "docker", Args: []string{"run"}})
	if err != nil {
		t.Fatalf("stdio: %v", err)
	}
	if _, ok := stdio.(*mcp.CommandTransport); !ok {
		t.Errorf("stdio: expected *CommandTransport, got %T", stdio)
	}
	http, err := transportFor(&ServerDef{ServerURL: "http://localhost:5001"})
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	if _, ok := http.(*mcp.StreamableClientTransport); !ok {
		t.Errorf("http: expected *StreamableClientTransport, got %T", http)
	}
	if _, err := transportFor(&ServerDef{}); err == nil {
		t.Error("expected error for empty ServerDef")
	}
}

func TestExpandArgs(t *testing.T) {
	t.Setenv("SERENA_TEST_VAR", "hello")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	got := expandArgs([]string{
		"$(pwd)",
		"$(pwd):/workspace",
		"$SERENA_TEST_VAR",
		"${SERENA_TEST_VAR}",
		"plain",
		"no$var",
	})
	want := []string{
		wd,
		wd + ":/workspace",
		"hello",
		"hello",
		"plain",
		"no",
	}
	if len(got) != len(want) {
		t.Fatalf("expandArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}