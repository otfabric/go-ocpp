// SPDX-License-Identifier: MIT

// Package engine is the OCPP-J RPC endpoint that the client and the server
// share: one Endpoint per WebSocket connection, whichever side opened it
// (ARCHITECTURE.md, "The engine"; ADR 0004).
//
// An Endpoint runs three goroutines: a reader, which decodes frames and
// routes them; a writer, the only one that writes to the connection; and a
// dispatcher, which handles incoming CALLs one at a time; with PingInterval
// a fourth, the pinger. Done and Close wait for these (see each). Outgoing
// CALLs take a single slot, because OCPP-J allows one outstanding CALL per
// direction (SPECIFICATIONS.md T6); callers wait for it in a bounded queue.
//
// Application code runs on these goroutines: the dispatcher's handlers and
// the handler interceptor on the dispatcher; and, on a goroutine of their
// own per handled CALL, the functions a handler registered with
// ocpp.AfterResponse. All of it is recovered where it is called when it does
// not return normally, logged with its stack, and the endpoint goes on. The
// frame observer and the metrics callbacks also run on them, and are not
// recovered.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/transport"
	"github.com/otfabric/go-ocpp/ocppj"
)

// Defaults for Config fields left zero.
const (
	DefaultResponseTimeout = 30 * time.Second
	DefaultMaxQueuedCalls  = 64
	DefaultWriteTimeout    = 10 * time.Second
)

// Config configures an Endpoint.
type Config struct {
	// Info identifies the connection in logs, metrics and interceptors.
	// Info.Version is the negotiated version and must be valid.
	Info ocpp.ConnInfo
	// Peer is what handlers receive as the other end of the connection.
	Peer ocpp.Peer
	// Dispatcher handles incoming CALLs; nil answers every CALL with
	// NotImplemented.
	Dispatcher ocpp.Dispatcher
	// CanSend reports whether this end may send an action; nil allows all.
	CanSend func(action string) bool

	ResponseTimeout time.Duration
	MaxQueuedCalls  int
	WriteTimeout    time.Duration
	Limits          ocppj.Limits
	// PingInterval is the time between WebSocket pings; a pong must arrive
	// within it, or the connection is lost. A ping sent while the reader
	// waits for room in the incoming queue is not counted (pingLoop). Zero:
	// no pings.
	PingInterval time.Duration
	// MaxBadMessages closes the connection (status 1008) once that many
	// malformed frames have been received on it. Zero: no limit.
	MaxBadMessages int
	// IncomingRate limits how many incoming CALLs and SENDs are dispatched
	// per second, with bursts of IncomingBurst (at least 1). Messages over
	// the rate wait; none is refused. Zero: no limit.
	IncomingRate  float64
	IncomingBurst int

	Logger             *slog.Logger // nil: discard
	Metrics            ocpp.Metrics // nil: none
	CallInterceptor    ocpp.Interceptor
	HandlerInterceptor ocpp.Interceptor
	FrameObserver      ocpp.FrameObserver
	// NewMessageID returns a message ID of at most 36 characters, unique on
	// the connection. Nil: a random UUID.
	NewMessageID func() string
}

// Endpoint is one OCPP-J RPC endpoint over one connection. It is safe for
// concurrent use.
type Endpoint struct {
	cfg  Config
	conn transport.Conn
	log  *slog.Logger

	ctx    context.Context // ends when the endpoint shuts down
	cancel context.CancelFunc

	slot    chan struct{} // holds a token while a CALL is outstanding
	waiting atomic.Int64  // callers waiting for the slot

	mu      sync.Mutex
	pending *pendingCall

	// unwritten counts the frames queued on results, calls and sends and not yet
	// written: a frame the writer has taken off its queue is still being written.
	unwritten atomic.Int64
	// handling counts the incoming CALLs and SENDs read and not yet handled:
	// queued, or in their handler. A CALL's answer is queued (unwritten) before
	// the CALL stops counting here.
	handling atomic.Int64
	// active counts the handlers that started before a drain, so that the
	// drain can tell whether one is running; draining refuses new outgoing
	// CALLs and SENDs and stops dispatching incoming CALLs that have not
	// started. Incoming SENDs dispatched while draining are not counted.
	active   atomic.Int64
	draining atomic.Bool

	results  chan []byte      // CALLRESULT and CALLERROR frames to write
	calls    chan outCall     // CALL frames to write
	sends    chan []byte      // SEND frames to write (2.1); they do not wait for the CALL slot
	incoming chan ocppj.Frame // CALLs to dispatch

	startOnce    sync.Once
	shutdownOnce sync.Once
	cause        error         // why the endpoint ended; set before ctx is cancelled
	ioDone       chan struct{} // closed when the reader, the writer and the pinger have exited
	done         chan struct{} // closed when the dispatcher has exited too (see Done)
	inHandler    atomic.Bool
	badMessages  atomic.Int64
	// readerWaits is odd while the reader waits for room in the incoming
	// queue, and changes each time that wait starts or ends (enqueue).
	readerWaits atomic.Int64
	limiter     *limiter // nil without IncomingRate
}

type pendingCall struct {
	id, action string
	sent       time.Time
	info       ocpp.CallInfo
	ch         chan callResult // buffered: receives exactly one result
	timer      *time.Timer
	// taken is set, under mu, when the writer takes the CALL's frame off its
	// queue. A call that finishes before that is not written at all.
	taken bool
}

// outCall is a CALL frame queued for the writer, with the call it belongs to.
type outCall struct {
	data []byte
	p    *pendingCall
}

type callResult struct {
	payload json.RawMessage
	err     error
}

// New returns an Endpoint on conn. It does nothing until Start, so that the
// owner can make the endpoint reachable (for handlers that call back)
// before the first frame is read.
func New(conn transport.Conn, cfg Config) *Endpoint {
	if cfg.ResponseTimeout <= 0 {
		cfg.ResponseTimeout = DefaultResponseTimeout
	}
	if cfg.MaxQueuedCalls <= 0 {
		cfg.MaxQueuedCalls = DefaultMaxQueuedCalls
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = DefaultWriteTimeout
	}
	if cfg.Metrics == nil {
		cfg.Metrics = ocpp.NopMetrics{}
	}
	if cfg.NewMessageID == nil {
		cfg.NewMessageID = NewUUID
	}
	var lim *limiter
	if cfg.IncomingRate > 0 {
		lim = newLimiter(cfg.IncomingRate, cfg.IncomingBurst)
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log = log.With("station_id", cfg.Info.StationID, "ocpp_version", cfg.Info.Version.String())
	if cfg.Info.SessionID != "" { // the server's; a client has none
		log = log.With("session_id", cfg.Info.SessionID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Endpoint{
		limiter:  lim,
		cfg:      cfg,
		conn:     conn,
		log:      log,
		ctx:      ctx,
		cancel:   cancel,
		slot:     make(chan struct{}, 1),
		results:  make(chan []byte, 8),
		calls:    make(chan outCall, 1),
		sends:    make(chan []byte, 16),
		incoming: make(chan ocppj.Frame, 16),
		ioDone:   make(chan struct{}),
		done:     make(chan struct{}),
	}
	return e
}

// Start starts the endpoint's goroutines. Calling it again does nothing.
func (e *Endpoint) Start() {
	e.startOnce.Do(func() {
		var io sync.WaitGroup
		io.Add(2)
		go func() { defer io.Done(); e.readLoop() }()
		go func() { defer io.Done(); e.writeLoop() }()
		if e.cfg.PingInterval > 0 {
			io.Go(func() { ; e.pingLoop() })
		}
		dispatched := make(chan struct{})
		go func() { defer close(dispatched); e.dispatchLoop() }()
		go func() {
			io.Wait()
			close(e.ioDone)
			<-dispatched
			close(e.done)
		}()
	})
}

// Info returns the connection's ConnInfo (Config.Info).
func (e *Endpoint) Info() ocpp.ConnInfo { return e.cfg.Info }

// Done is closed when the endpoint has shut down and its goroutines have
// returned: the reader, the writer, the pinger and the dispatcher, with the
// handler the dispatcher is running and the SENDs it still delivers (Drain).
//
// It does not wait for the functions registered with ocpp.AfterResponse:
// they are application code on goroutines of their own, and may close or
// shut down the endpoint themselves, which a wait for them would never
// survive. The calls they make on an ended endpoint fail at once. Nor does
// it wait for the goroutine of a CloseWith, which only runs shutdown: that
// has closed the connection before any of the four can return.
func (e *Endpoint) Done() <-chan struct{} { return e.done }

// Err returns why the endpoint shut down: ocpp.ErrClosed after Close,
// otherwise an error wrapping ocpp.ErrConnectionLost. It returns nil while
// the endpoint runs.
func (e *Endpoint) Err() error {
	select {
	case <-e.ctx.Done():
		return e.cause
	default:
		return nil
	}
}

// Close closes the connection with a normal closure at once: frames still
// queued or being written are dropped (Shutdown waits for them). It returns once the
// connection is closed and the reader, the writer and the pinger have
// returned, and, unless a handler is running, the dispatcher as well; Done
// reports when the dispatcher has returned too. Like Done, it does not wait
// for functions registered with ocpp.AfterResponse, which may call it.
//
// Close is safe to call more than once and from multiple goroutines: every
// call returns nil, after the same wait. Close must not be called from a
// FrameObserver.
func (e *Endpoint) Close() error {
	e.shutdown(ocpp.ErrClosed, transport.StatusNormal, "")
	e.Start() // so that Done is closed for an endpoint that never started
	<-e.ioDone
	if !e.inHandler.Load() {
		<-e.done
	}
	return nil
}

// Idle reports whether nothing is in flight: no outgoing CALL is outstanding
// or waiting for the slot, no frame (CALL, SEND, CALLRESULT, CALLERROR) is
// queued or being written, and no incoming CALL is queued or being handled.
// While draining, incoming CALLs that have not started do not count: they are
// not dispatched any more; nor do incoming SENDs dispatched after Drain, so
// that a peer that keeps sending cannot keep the drain from ending (they are
// still delivered, see Drain). It is what a graceful shutdown waits for.
func (e *Endpoint) Idle() bool {
	e.mu.Lock()
	pending := e.pending != nil
	e.mu.Unlock()
	incoming := e.handling.Load()
	if e.draining.Load() {
		incoming = e.active.Load()
	}
	return !pending && e.waiting.Load() == 0 && e.unwritten.Load() == 0 && incoming == 0
}

// Drain starts a graceful shutdown: new outgoing CALLs and SENDs fail with
// ocpp.ErrClosed, and incoming CALLs that have not started are no longer
// dispatched (they get no answer, as after Close). What is in flight goes on:
// wait for Idle, then Close. A connection busy in both directions reaches
// Idle once the CALLs in flight are answered. Incoming SENDs are still
// delivered, with a context that Close does not cancel, but those dispatched
// after Drain do not keep the endpoint from being Idle: the SENDs received
// before Close are delivered after it, and Done reports when they have been.
func (e *Endpoint) Drain() { e.draining.Store(true) }

// HandlerRunning reports whether a handler is running. Owners use it to
// avoid waiting for a handler from inside that handler.
func (e *Endpoint) HandlerRunning() bool { return e.inHandler.Load() }

// CloseWith closes the connection with the given WebSocket status and
// reason, and with cause as Err. It does not wait.
func (e *Endpoint) CloseWith(cause error, code int, reason string) {
	go func() {
		e.shutdown(cause, code, reason)
		e.Start()
	}()
}

// shutdown ends the endpoint with cause. A code of zero aborts the
// connection without the closing handshake, for a peer that no longer
// responds (no pong, a write that timed out): waiting for its close frame
// would only delay noticing the loss.
func (e *Endpoint) shutdown(cause error, code int, reason string) {
	e.shutdownOnce.Do(func() {
		e.cause = cause
		// Close the WebSocket before cancelling: the reader must still be
		// reading to complete the closing handshake.
		if code == 0 {
			e.conn.Abort()
		} else {
			_ = e.conn.Close(code, reason)
		}
		e.cancel()
		e.mu.Lock()
		p := e.pending
		e.mu.Unlock()
		if p != nil {
			e.finish(p, callResult{err: cause})
		}
		if errors.Is(cause, ocpp.ErrClosed) {
			e.log.Debug("connection closed")
		} else {
			e.log.Info("connection lost", "error", cause)
		}
	})
}

// lost wraps err as a lost connection.
func lost(err error) error {
	if errors.Is(err, ocpp.ErrConnectionLost) {
		return err
	}
	return fmt.Errorf("%w: %w", ocpp.ErrConnectionLost, err)
}

// NewUUID returns a random (version 4) UUID, 36 characters long.
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:36], b[10:16])
	return string(s[:])
}
