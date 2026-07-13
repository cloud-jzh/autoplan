package terminal

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// contractPTY is deliberately in-memory: P14 unit contracts must not start a
// shell, inherit an environment, or touch a developer workspace.
type contractPTY struct {
	mu       sync.Mutex
	read     []byte
	signals  []Signal
	resizes  [][2]int
	writes   [][]byte
	kills    int
	closed   int
	wait     chan struct{}
	waitOnce sync.Once
	waitExit Exit
}

func newContractPTY() *contractPTY {
	return &contractPTY{wait: make(chan struct{}), waitExit: Exit{Code: 137}}
}

func (pty *contractPTY) Read(buffer []byte) (int, error) {
	pty.mu.Lock()
	defer pty.mu.Unlock()
	if len(pty.read) == 0 {
		return 0, io.EOF
	}
	count := copy(buffer, pty.read)
	pty.read = pty.read[count:]
	return count, nil
}

func (pty *contractPTY) Write(data []byte) (int, error) {
	pty.mu.Lock()
	defer pty.mu.Unlock()
	pty.writes = append(pty.writes, append([]byte{}, data...))
	return len(data), nil
}

func (pty *contractPTY) Resize(cols, rows int) error {
	pty.mu.Lock()
	defer pty.mu.Unlock()
	pty.resizes = append(pty.resizes, [2]int{cols, rows})
	return nil
}

func (pty *contractPTY) Signal(signal Signal) error {
	pty.mu.Lock()
	defer pty.mu.Unlock()
	pty.signals = append(pty.signals, signal)
	return nil
}

func (pty *contractPTY) Kill() error {
	pty.mu.Lock()
	pty.kills++
	pty.mu.Unlock()
	pty.waitOnce.Do(func() { close(pty.wait) })
	return nil
}

func (pty *contractPTY) Wait(ctx context.Context) (Exit, error) {
	select {
	case <-ctx.Done():
		return Exit{}, ctx.Err()
	case <-pty.wait:
		pty.mu.Lock()
		defer pty.mu.Unlock()
		exit := pty.waitExit
		exit.EndedAt = time.Now().UTC()
		return exit, nil
	}
}

func (pty *contractPTY) Close() error {
	pty.mu.Lock()
	pty.closed++
	pty.mu.Unlock()
	return nil
}

func (pty *contractPTY) PID() int { return 0 }

func terminalContractLimits() Limits {
	return Limits{
		MaxSessionsGlobal: 2, MaxSessionsPerProject: 1, MaxSessionRuntime: time.Second, GracePeriod: time.Millisecond,
		MaxInputBytes: 4, MaxInputRateBytes: 4, InputRateWindow: time.Hour, MaxResizeRate: 1, ResizeRateWindow: time.Hour,
		ReadChunkBytes: 2, DefaultCols: 80, DefaultRows: 24,
	}
}

func TestPTYSessionBoundsInputResizeAndReadChunks(t *testing.T) {
	pty := newContractPTY()
	pty.read = []byte("output")
	session := newSession(pty, terminalContractLimits())

	if written, err := session.Write([]byte("four")); err != nil || written != 4 {
		t.Fatalf("first write = %d, %v", written, err)
	}
	if _, err := session.Write([]byte("x")); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("rate-limited write error = %v, want ErrInputLimit", err)
	}
	if err := session.Resize(80, 24); err != nil {
		t.Fatalf("first resize = %v", err)
	}
	if err := session.Resize(81, 24); !errors.Is(err, ErrResizeLimit) {
		t.Fatalf("second resize error = %v, want ErrResizeLimit", err)
	}
	buffer := make([]byte, 32)
	if read, err := session.Read(buffer); err != nil || read != 2 || string(buffer[:read]) != "ou" {
		t.Fatalf("bounded read = %d, %q, %v", read, string(buffer[:read]), err)
	}
	if err := session.Resize(1, 24); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid resize error = %v, want ErrInvalidRequest", err)
	}
}

func TestPTYSessionCloseHasOneTerminationPathAndOneExit(t *testing.T) {
	pty := newContractPTY()
	session := newSession(pty, terminalContractLimits())
	session.start(nil)
	if err := session.Close(); err != nil {
		t.Fatalf("close error = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second close error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	exit, err := session.Wait(ctx)
	if err != nil || exit.Code != 137 || exit.EndedAt.IsZero() {
		t.Fatalf("terminal exit = %#v, %v", exit, err)
	}
	pty.mu.Lock()
	defer pty.mu.Unlock()
	if len(pty.signals) != 1 || pty.signals[0] != SignalTerminate || pty.kills != 1 || pty.closed != 1 {
		t.Fatalf("termination calls signals=%#v kills=%d closes=%d", pty.signals, pty.kills, pty.closed)
	}
}
