package terminal

import (
	"context"
	"sync"
	"time"
)

// Session serializes lifecycle transitions around one PTY. It creates exactly
// one wait path and one exit result even when kill, close, timeout, context
// cancellation and platform exit race with one another.
type Session struct {
	pty    PTY
	limits Limits

	mu           sync.Mutex
	started      bool
	closed       bool
	timedOut     bool
	inputWindow  rateWindow
	resizeWindow rateWindow
	exit         Exit
	waitErr      error
	done         chan struct{}
	closeTimer   *time.Timer
	startOnce    sync.Once
	closeOnce    sync.Once
}

type rateWindow struct {
	started time.Time
	used    int
	count   int
}

func newSession(pty PTY, limits Limits) *Session {
	return &Session{pty: pty, limits: limits, done: make(chan struct{})}
}

func (session *Session) PID() int {
	if session == nil || session.pty == nil {
		return 0
	}
	return session.pty.PID()
}

func (session *Session) Read(buffer []byte) (int, error) {
	if session == nil || session.pty == nil {
		return 0, ErrSessionClosed
	}
	if len(buffer) > session.limits.ReadChunkBytes {
		buffer = buffer[:session.limits.ReadChunkBytes]
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	return session.pty.Read(buffer)
}

func (session *Session) Write(data []byte) (int, error) {
	if session == nil || session.pty == nil {
		return 0, ErrSessionClosed
	}
	if len(data) == 0 {
		return 0, nil
	}
	if len(data) > session.limits.MaxInputBytes {
		return 0, ErrInputLimit
	}
	if !session.reserveInput(len(data), time.Now()) {
		return 0, ErrInputLimit
	}
	session.mu.Lock()
	closed := session.closed
	session.mu.Unlock()
	if closed {
		return 0, ErrSessionClosed
	}
	return session.pty.Write(data)
}

func (session *Session) Resize(cols, rows int) error {
	if session == nil || session.pty == nil {
		return ErrSessionClosed
	}
	if !session.limits.validSize(cols, rows) {
		return ErrInvalidRequest
	}
	if !session.reserveResize(time.Now()) {
		return ErrResizeLimit
	}
	session.mu.Lock()
	closed := session.closed
	session.mu.Unlock()
	if closed {
		return ErrSessionClosed
	}
	if err := session.pty.Resize(cols, rows); err != nil {
		return ErrPlatformUnavailable
	}
	return nil
}

func (session *Session) Signal(signal Signal) error {
	if session == nil || session.pty == nil {
		return ErrSessionClosed
	}
	session.mu.Lock()
	closed := session.closed
	session.mu.Unlock()
	if closed {
		return ErrSessionClosed
	}
	if err := session.pty.Signal(signal); err != nil {
		return ErrPlatformUnavailable
	}
	return nil
}

func (session *Session) Kill() error {
	if session == nil || session.pty == nil {
		return ErrSessionClosed
	}
	session.mu.Lock()
	closed := session.closed
	session.mu.Unlock()
	if closed {
		return nil
	}
	if err := session.pty.Kill(); err != nil {
		return ErrPlatformUnavailable
	}
	return nil
}

// Close first requests a process-tree termination, then forces termination
// after the configured grace period. Completion is still reported by the one
// Wait path, so close cannot create a second terminal event.
func (session *Session) Close() error {
	if session == nil || session.pty == nil {
		return nil
	}
	var closeErr error
	session.closeOnce.Do(func() {
		session.mu.Lock()
		session.closed = true
		session.mu.Unlock()
		if err := session.pty.Signal(SignalTerminate); err != nil {
			closeErr = ErrPlatformUnavailable
			// A failed graceful path must never leave the tree running merely
			// because the daemon is closing. The platform adapter still returns
			// a stable failure while this best-effort hard stop runs.
			_ = session.pty.Kill()
		}
		grace := time.AfterFunc(session.limits.GracePeriod, func() {
			select {
			case <-session.done:
				return
			default:
				_ = session.pty.Kill()
			}
		})
		session.mu.Lock()
		session.closeTimer = grace
		session.mu.Unlock()
	})
	return closeErr
}

func (session *Session) Wait(ctx context.Context) (Exit, error) {
	if session == nil || session.pty == nil {
		return Exit{}, ErrSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		_ = session.Close()
		return Exit{}, ErrSessionClosed
	case <-session.done:
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.exit, session.waitErr
	}
}

func (session *Session) start(release func()) {
	if session == nil {
		return
	}
	session.startOnce.Do(func() {
		session.mu.Lock()
		session.started = true
		session.mu.Unlock()
		timeout := time.AfterFunc(session.limits.MaxSessionRuntime, func() {
			session.mu.Lock()
			session.timedOut = true
			session.mu.Unlock()
			_ = session.Kill()
		})
		go func() {
			exit, err := session.pty.Wait(context.Background())
			timeout.Stop()
			_ = session.pty.Close()
			session.mu.Lock()
			if session.closeTimer != nil {
				session.closeTimer.Stop()
			}
			exit.TimedOut = exit.TimedOut || session.timedOut
			if exit.EndedAt.IsZero() {
				exit.EndedAt = time.Now().UTC()
			}
			session.exit, session.waitErr = exit, err
			session.closed = true
			session.mu.Unlock()
			close(session.done)
			if release != nil {
				release()
			}
		}()
	})
}

func (session *Session) watchContext(ctx context.Context) {
	if session == nil || ctx == nil || ctx.Done() == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-session.done:
		}
	}()
}

func (session *Session) reserveInput(size int, now time.Time) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return false
	}
	if session.inputWindow.started.IsZero() || now.Sub(session.inputWindow.started) >= session.limits.InputRateWindow {
		session.inputWindow = rateWindow{started: now}
	}
	if session.inputWindow.used+size > session.limits.MaxInputRateBytes {
		return false
	}
	session.inputWindow.used += size
	return true
}

func (session *Session) reserveResize(now time.Time) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return false
	}
	if session.resizeWindow.started.IsZero() || now.Sub(session.resizeWindow.started) >= session.limits.ResizeRateWindow {
		session.resizeWindow = rateWindow{started: now}
	}
	if session.resizeWindow.count >= session.limits.MaxResizeRate {
		return false
	}
	session.resizeWindow.count++
	return true
}
