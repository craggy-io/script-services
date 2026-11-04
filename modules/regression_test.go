package modules_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/modules"
)

// AGENTS.md promises a nil context is context.Background() for
// Loader.Module and Loader.Bind; both used to panic.
func TestNilContext(t *testing.T) {
	l := modules.NewLoader(modules.Map(map[string]string{"a.star": "x = 1\n"}), modules.Options{})
	//nolint:staticcheck // deliberately nil
	if _, _, err := l.Module(nil, "a.star"); err != nil {
		t.Fatalf("Module(nil) = %v", err)
	}

	l2 := modules.NewLoader(modules.Map(map[string]string{"a.star": "x = 2\n"}), modules.Options{})
	th := &starlark.Thread{}
	//nolint:staticcheck // deliberately nil
	l2.Bind(nil, th, "main.star")
	g, err := th.Load(th, "./a.star")
	if err != nil || g["x"] != starlark.MakeInt(2) {
		t.Fatalf("Bind(nil) load = %v, %v", g, err)
	}
}

type panicSource struct {
	mu    sync.Mutex
	panic bool
}

func (s *panicSource) Read(ctx context.Context, p string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panic {
		panic("source boom")
	}
	return []byte("x = 1\n"), nil
}

func (s *panicSource) setPanic(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panic = v
}

// A panicking Source must not wedge the Loader's entry for that path:
// once the panic is recovered by the caller, a later load retries.
func TestPanickingSourceDoesNotWedgeLoader(t *testing.T) {
	src := &panicSource{panic: true}
	l := modules.NewLoader(src, modules.Options{})
	func() {
		defer func() { _ = recover() }()
		_, _, _ = l.Module(context.Background(), "a.star")
		t.Fatal("expected panic")
	}()
	src.setPanic(false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g, _, err := l.Module(ctx, "a.star")
	if err != nil || g["x"] != starlark.MakeInt(1) {
		t.Fatalf("retry after panic = %v, %v", g, err)
	}
}

type gatedSource struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *gatedSource) Read(ctx context.Context, p string) ([]byte, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return []byte("x = 1\n"), nil
}

// A waiter whose context ends while another load is in progress gets a
// *modules.Error wrapping the context error, like the creator path —
// not a bare context error.
func TestWaiterCancellationIsModuleError(t *testing.T) {
	src := &gatedSource{entered: make(chan struct{}), release: make(chan struct{})}
	l := modules.NewLoader(src, modules.Options{})
	done := make(chan error, 1)
	go func() {
		_, _, err := l.Module(context.Background(), "a.star")
		done <- err
	}()
	<-src.entered
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() {
		_, _, err := l.Module(ctx, "a.star")
		waiter <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the waiter block on the entry
	cancel()
	err := <-waiter
	var me *modules.Error
	if !errors.As(err, &me) || !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter err = %v, want *modules.Error wrapping context.Canceled", err)
	}
	close(src.release)
	if err := <-done; err != nil {
		t.Fatalf("creator err = %v", err)
	}
}
