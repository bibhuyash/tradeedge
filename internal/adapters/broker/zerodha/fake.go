package zerodha

import (
	"context"
	"sync"
)

type FakeRead struct {
	Body   []byte
	Status int
	Err    error
}

// FakeTransport is deterministic, bounded, concurrency-safe, and contains no network path.
type FakeTransport struct {
	mu           sync.Mutex
	profiles     []FakeRead
	instruments  []FakeRead
	profileAt    int
	instrumentAt int
	closed       bool
}

func NewFakeTransport(profiles, instruments []FakeRead) *FakeTransport {
	return &FakeTransport{profiles: append([]FakeRead(nil), profiles...), instruments: append([]FakeRead(nil), instruments...)}
}

func (fake *FakeTransport) Profile(ctx context.Context, _ string) ([]byte, int, error) {
	return fake.next(ctx, fake.profiles, &fake.profileAt)
}

func (fake *FakeTransport) Instruments(ctx context.Context, _ string) ([]byte, int, error) {
	return fake.next(ctx, fake.instruments, &fake.instrumentAt)
}

func (fake *FakeTransport) next(ctx context.Context, values []FakeRead, cursor *int) ([]byte, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.closed {
		return nil, 0, ErrStopped
	}
	if *cursor >= len(values) {
		return nil, 0, ErrUnavailable
	}
	value := values[*cursor]
	*cursor++
	return append([]byte(nil), value.Body...), value.Status, value.Err
}

func (fake *FakeTransport) CloseIdleConnections() {
	fake.mu.Lock()
	fake.closed = true
	fake.mu.Unlock()
}

func (fake *FakeTransport) Calls() (profiles, instruments int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.profileAt, fake.instrumentAt
}
