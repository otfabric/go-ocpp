// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/after"
	"github.com/otfabric/go-ocpp/internal/transport"
	"github.com/otfabric/go-ocpp/ocppj"
)

func (e *Endpoint) readLoop() {
	for {
		data, err := e.conn.ReadMessage(e.ctx)
		if err != nil {
			if ce, ok := errors.AsType[*transport.CloseError](err); ok {
				e.shutdown(lost(&ocpp.CloseError{Code: ce.Code, Reason: ce.Reason}), ce.Code, "")
			} else {
				e.shutdown(lost(err), transport.StatusInternalError, "")
			}
			return
		}
		if obs := e.cfg.FrameObserver; obs != nil {
			obs(e.cfg.Info, ocpp.Incoming, data)
		}
		f, err := ocppj.Decode(data, e.cfg.Info.Version, e.cfg.Limits)
		if err != nil {
			e.malformed(err)
			continue
		}
		switch f.Type {
		case ocppj.Call, ocppj.Send:
			// SENDs are handled in order with the CALLs, and never answered.
			e.handling.Add(1)
			if !e.enqueue(f) {
				e.handling.Add(-1)
				return
			}
		case ocppj.CallResult, ocppj.CallError:
			e.respond(f)
		case ocppj.CallResultError:
			// The peer could not process a CALLRESULT of ours (2.1 Part 4
			// §4.2.3). There is nothing to answer; it is reported.
			ce := &ocpp.CallError{Code: f.ErrorCode, Description: f.Description, Details: f.Details}
			err := fmt.Errorf("%w: the peer could not process the response to message %q: %w", ocpp.ErrProtocol, f.ID, ce)
			e.cfg.Metrics.OnProtocolError(e.cfg.Info, err)
			e.log.Warn("response refused by the peer", "message_id", f.ID, "error_code", string(f.ErrorCode), "error", err)
		}
	}
}

// enqueue hands an incoming CALL or SEND to the dispatcher, and reports false
// when the endpoint ended first. When the queue is full the reader waits
// here for room and reads nothing meanwhile: no pong either, which
// readerWaits tells pingLoop (odd while the reader waits).
func (e *Endpoint) enqueue(f ocppj.Frame) bool {
	select {
	case e.incoming <- f:
		return true
	default:
	}
	e.readerWaits.Add(1)
	defer e.readerWaits.Add(1)
	select {
	case e.incoming <- f:
		return true
	case <-e.ctx.Done():
		return false
	}
}

// malformed handles a frame that does not decode (SPECIFICATIONS.md T3, T9):
//
//   - A frame of an unknown message type is ignored (2.x Part 4 §4.1.3 as
//     amended by errata 1161 and 1163; OCPP-J 1.6 says nothing else).
//   - A corrupt CALL, or a frame whose message type cannot be read, is
//     answered with a CALLERROR: with its message ID, or, in OCPP 2.x, with
//     "-1" when the ID cannot be read either (2.x Part 4 §4.2.3). OCPP-J 1.6
//     defines no ID for that case, so on 1.6 such a frame is dropped.
//   - A malformed CALLRESULT or CALLERROR is dropped: a CALLERROR in reply
//     would answer a message that is not a CALL.
func (e *Endpoint) malformed(err error) {
	err = fmt.Errorf("%w: %w", ocpp.ErrProtocol, err)
	e.cfg.Metrics.OnProtocolError(e.cfg.Info, err)
	if n := e.badMessages.Add(1); e.cfg.MaxBadMessages > 0 && n >= int64(e.cfg.MaxBadMessages) {
		if n == int64(e.cfg.MaxBadMessages) {
			e.log.Warn("closing: too many malformed frames", "count", n, "error", err)
			e.CloseWith(lost(fmt.Errorf("%w: %d malformed frames received", ocpp.ErrProtocol, n)),
				transport.StatusPolicy, "too many malformed messages")
		}
		return
	}
	var de *ocppj.DecodeError
	errors.As(err, &de)
	if de == nil || de.UnknownType || (de.Type != ocppj.Call && de.Type != 0) {
		e.log.Warn("malformed frame dropped", "error", err)
		return
	}
	if e.draining.Load() {
		// While draining, CALLs that have not started get no answer (Drain);
		// a malformed one gets none either, so that a peer cannot keep the
		// drain from ending by sending them.
		e.log.Warn("malformed frame dropped while draining", "error", err)
		return
	}
	id := de.ID
	if !de.IDKnown {
		if e.cfg.Info.Version == ocpp.V16 {
			e.log.Warn("malformed frame dropped", "error", err)
			return
		}
		id = "-1"
	}
	e.log.Warn("malformed frame answered with CALLERROR", "message_id", id, "error", err)
	e.sendResult(ocppj.Frame{Type: ocppj.CallError, ID: id,
		ErrorCode: ocppj.RPCErrorCode(e.cfg.Info.Version), Description: e.limit(de.Reason)})
}

// pingLoop sends a WebSocket ping every PingInterval. A pong that does not
// arrive within the interval means the peer is unreachable (a half-open
// TCP connection, say), and the connection is closed as lost.
//
// A pong is read by the reader only. A ping whose interval the reader spent,
// in whole or in part, waiting for room in the incoming queue (enqueue) says
// nothing about the peer: it is not counted, and the next ping goes out an
// interval later. A peer that is unreachable is then noticed once the reader
// reads again.
func (e *Endpoint) pingLoop() {
	t := time.NewTicker(e.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
		}
		before := e.readerWaits.Load()
		ctx, cancel := context.WithTimeout(e.ctx, e.cfg.PingInterval)
		err := e.conn.Ping(ctx)
		cancel()
		if err != nil && e.ctx.Err() == nil {
			if errors.Is(err, context.DeadlineExceeded) && (before%2 == 1 || e.readerWaits.Load() != before) {
				e.log.Debug("pong not awaited: reading waits for the incoming queue", "interval", e.cfg.PingInterval)
				continue
			}
			e.log.Warn("no pong: closing", "error", err)
			e.CloseWith(lost(fmt.Errorf("no pong within %s: %w", e.cfg.PingInterval, err)), 0, "")
			return
		}
	}
}

func (e *Endpoint) writeLoop() {
	for {
		// Results of incoming CALLs go before new outgoing CALLs.
		var data []byte
		select {
		case data = <-e.results:
		default:
			select {
			case data = <-e.results:
			case c := <-e.calls:
				if !e.take(c.p) {
					// The call ended (timed out, or its context ended) before
					// its frame was taken: its caller has been told it failed,
					// so the CALL is not sent.
					e.unwritten.Add(-1)
					continue
				}
				data = c.data
			case data = <-e.sends:
			case <-e.ctx.Done():
				return
			}
		}
		ctx, cancel := context.WithTimeout(e.ctx, e.cfg.WriteTimeout)
		err := e.conn.WriteMessage(ctx, data)
		cancel()
		if err == nil {
			if obs := e.cfg.FrameObserver; obs != nil {
				obs(e.cfg.Info, ocpp.Outgoing, data)
			}
		}
		e.unwritten.Add(-1)
		if err != nil {
			e.shutdown(lost(fmt.Errorf("write: %w", err)), 0, "")
			return
		}
	}
}

func (e *Endpoint) sendResult(f ocppj.Frame) {
	data, err := ocppj.Encode(f)
	if err != nil {
		e.log.Error("cannot encode a response", "message_id", f.ID, "error", err)
		return
	}
	e.unwritten.Add(1)
	select {
	case e.results <- data:
	case <-e.ctx.Done():
		e.unwritten.Add(-1)
	}
}

func (e *Endpoint) dispatchLoop() {
	for {
		select {
		case f := <-e.incoming:
			if e.limiter != nil && !e.limiter.wait(e.ctx) {
				// Ended while waiting: deliver this SEND and those queued.
				if f.Type == ocppj.Send {
					e.inHandler.Store(true)
					e.handleSend(context.WithoutCancel(e.ctx), f)
					e.inHandler.Store(false)
				}
				e.handling.Add(-1)
				e.deliverReceivedSends()
				return
			}
			e.active.Add(1)
			if e.draining.Load() || e.ctx.Err() != nil {
				// A CALL that has not started is not answered any more, while
				// draining or once the endpoint has ended. A SEND needs no
				// answer and is delivered, but does not count as in flight: a
				// peer that keeps sending must not keep a drain from ending.
				// Its handler gets a context that the Close ending the drain
				// does not cancel, as for the SENDs delivered after the end.
				e.active.Add(-1)
				if f.Type == ocppj.Send {
					e.inHandler.Store(true)
					e.handleSend(context.WithoutCancel(e.ctx), f)
					e.inHandler.Store(false)
				}
				e.handling.Add(-1)
				continue
			}
			e.inHandler.Store(true)
			if f.Type == ocppj.Send {
				e.handleSend(e.ctx, f)
			} else {
				e.handle(f)
			}
			e.inHandler.Store(false)
			e.active.Add(-1)
			e.handling.Add(-1)
		case <-e.ctx.Done():
			e.deliverReceivedSends()
			return
		}
	}
}

// deliverReceivedSends handles the SENDs that were received in full before the
// endpoint ended (a station that sends and closes: the close frame can be read
// before the SEND is dispatched). A SEND needs no answer, so it is delivered,
// with a context that is not cancelled; CALLs still queued are dropped, since
// they can no longer be answered.
func (e *Endpoint) deliverReceivedSends() {
	ctx := context.WithoutCancel(e.ctx)
	for {
		select {
		case f := <-e.incoming:
			if f.Type == ocppj.Send {
				e.inHandler.Store(true)
				e.handleSend(ctx, f)
				e.inHandler.Store(false)
			}
			e.handling.Add(-1)
		default:
			return
		}
	}
}

// handleSend runs the dispatcher for one incoming SEND (2.1). A SEND is
// never answered (2.1 Part 4 §4.2.4): errors are logged and counted only.
func (e *Endpoint) handleSend(ctx context.Context, f ocppj.Frame) {
	start := time.Now()
	info := ocpp.CallInfo{ConnInfo: e.cfg.Info, Action: f.Action, MessageID: f.ID, Direction: ocpp.Incoming}
	run := func(ctx context.Context) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = e.recovered(f, r)
			}
		}()
		sd, ok := e.cfg.Dispatcher.(ocpp.SendDispatcher)
		if !ok {
			return fmt.Errorf("no handler for SEND messages")
		}
		return sd.DispatchSend(ctx, e.cfg.Peer, f.Action, f.Payload)
	}
	var err error
	if e.cfg.HandlerInterceptor != nil {
		err = e.intercept(ctx, info, f, run)
	} else {
		err = run(ctx)
	}
	e.cfg.Metrics.OnCallHandled(e.cfg.Info, f.Action, time.Since(start), err)
	if err != nil {
		e.log.Warn("SEND not processed", "action", f.Action, "message_id", f.ID, "error", err)
		return
	}
	e.log.Debug("SEND handled", "action", f.Action, "message_id", f.ID, "direction", "incoming", "duration", time.Since(start))
}

// handle runs the dispatcher for one incoming CALL and sends its answer.
func (e *Endpoint) handle(f ocppj.Frame) {
	start := time.Now()
	info := ocpp.CallInfo{ConnInfo: e.cfg.Info, Action: f.Action, MessageID: f.ID, Direction: ocpp.Incoming}
	hctx, afterList := after.With(e.ctx)
	var result json.RawMessage
	run := func(ctx context.Context) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = e.recovered(f, r)
			}
		}()
		if e.cfg.Dispatcher == nil {
			return ocpp.NewCallError(ocpp.NotImplemented, "")
		}
		result, err = e.cfg.Dispatcher.Dispatch(ctx, e.cfg.Peer, f.Action, f.Payload)
		return err
	}
	var err error
	if e.cfg.HandlerInterceptor != nil {
		err = e.intercept(hctx, info, f, run)
	} else {
		err = run(hctx)
	}
	// Functions registered with ocpp.AfterResponse run once the result is
	// queued: the writer sends queued results before new CALLs, so what
	// they send goes out after the response.
	defer func() {
		if fs := afterList.Take(); len(fs) > 0 {
			go e.runAfter(f, fs)
		}
	}()
	if err == nil && !isObject(result) {
		err = errors.New("handler returned no response object")
	}
	e.cfg.Metrics.OnCallHandled(e.cfg.Info, f.Action, time.Since(start), err)
	if err == nil {
		e.log.Debug("call handled", "action", f.Action, "message_id", f.ID, "direction", "incoming", "duration", time.Since(start))
		e.sendResult(ocppj.Frame{Type: ocppj.CallResult, ID: f.ID, Payload: result})
		return
	}
	e.sendResult(ocppj.Frame{Type: ocppj.CallError, ID: f.ID, ErrorCode: e.errorCode(err, f), Description: e.limit(e.description(err)),
		Details: e.details(err)})
}

// details returns the CALLERROR details: a *ocpp.CallError's own, when they
// are a JSON object and its code is one the version defines (otherwise the
// error is answered as InternalError, without its text); {} else.
func (e *Endpoint) details(err error) json.RawMessage {
	var ce *ocpp.CallError
	if errors.As(err, &ce) && ocppj.ValidErrorCode(e.cfg.Info.Version, ce.Code) && isObject(ce.Details) {
		return ce.Details
	}
	return nil
}

// intercept runs the handler interceptor around run, for the incoming CALL
// or SEND f. The interceptor is application code on the dispatcher
// goroutine: when it does not return normally it is recovered here, logged
// with its stack, and its error is that of a handler that did not return
// (ocpp.ErrHandlerPanic): a CALL is answered with InternalError, and the
// dispatcher goes on with the next message.
func (e *Endpoint) intercept(ctx context.Context, info ocpp.CallInfo, f ocppj.Frame, run func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = e.recoveredFrom("handler interceptor did not return normally", f, r)
		}
	}()
	return e.cfg.HandlerInterceptor(ctx, info, run)
}

// runAfter runs the functions that the handler of the CALL f registered with
// ocpp.AfterResponse, in order, on the goroutine it is started on. Each is
// application code: one that does not return normally is recovered, logged
// with its stack, and the next one runs. Neither Done nor Close waits for
// this goroutine (see Done).
func (e *Endpoint) runAfter(f ocppj.Frame, fs []func()) {
	for _, fn := range fs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					_ = e.recoveredFrom("function registered with AfterResponse did not return normally", f, r)
				}
			}()
			fn()
		}()
	}
}

// recovered logs a handler panic with its stack and returns the error that
// reports it.
func (e *Endpoint) recovered(f ocppj.Frame, r any) error {
	e.log.Error("handler panic", "action", f.Action, "message_id", f.ID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
	return fmt.Errorf("%w: %v", ocpp.ErrHandlerPanic, r)
}

// recoveredFrom is recovered for the application code around a handler: it
// logs, under msg, the value r that code called for the incoming message f
// was recovered from, with the stack at the point of recovery and the
// attributes of recovered, and returns the same error. No lock of the
// endpoint is held where it is called.
func (e *Endpoint) recoveredFrom(msg string, f ocppj.Frame, r any) error {
	e.log.Error(msg, "action", f.Action, "message_id", f.ID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
	return fmt.Errorf("%w: %v", ocpp.ErrHandlerPanic, r)
}

// limit cuts an error description to the 255 characters OCPP 2.x allows
// (Part 4 §4.2.3, ErrorDescription string[255]). OCPP-J 1.6 sets no limit.
func (e *Endpoint) limit(desc string) string {
	if e.cfg.Info.Version == ocpp.V16 {
		return desc
	}
	if r := []rune(desc); len(r) > 255 {
		return string(r[:255])
	}
	return desc
}

// errorCode chooses the CALLERROR code for a handler error. Only a
// *ocpp.CallError with a code the version defines is sent as is.
func (e *Endpoint) errorCode(err error, f ocppj.Frame) ocpp.ErrorCode {
	if ce, ok := errors.AsType[*ocpp.CallError](err); ok {
		if ocppj.ValidErrorCode(e.cfg.Info.Version, ce.Code) {
			if ce.Code != ocpp.NotImplemented {
				e.log.Info("call answered with CALLERROR", "action", f.Action, "message_id", f.ID, "error_code", string(ce.Code))
			}
			return ce.Code
		}
		e.log.Error("handler returned an error code the version does not define; sent InternalError",
			"action", f.Action, "message_id", f.ID, "error_code", string(ce.Code))
		return ocpp.InternalError
	}
	e.log.Error("handler failed; sent InternalError", "action", f.Action, "message_id", f.ID, "error", err)
	return ocpp.InternalError
}

// description returns the CALLERROR description. Only a *ocpp.CallError's
// own description is sent: other error texts stay in the local log.
func (e *Endpoint) description(err error) string {
	var ce *ocpp.CallError
	if errors.As(err, &ce) && ocppj.ValidErrorCode(e.cfg.Info.Version, ce.Code) {
		return ce.Description
	}
	return ""
}

func isObject(m json.RawMessage) bool {
	for _, c := range m {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return json.Valid(m)
		default:
			return false
		}
	}
	return false
}
