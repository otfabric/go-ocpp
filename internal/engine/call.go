// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/ocppj"
)

// Call sends a CALL and waits for its CALLRESULT or CALLERROR.
//
// It waits for the outgoing slot first: OCPP-J allows one outstanding CALL
// per direction. When ctx ends, Call returns ctx.Err() at once, but a CALL
// already sent keeps the slot until its response arrives or the response
// timeout expires, as T6 requires. A CALL whose Call has returned (ctx ended,
// or no response within the timeout) before the writer took it off its queue
// is not sent at all.
//
// Every Call that gets past the checks before the queue (draining, the
// direction, marshalling req) is reported once to Metrics.OnCallDone, with
// the error it returns: after the response has been decoded and validated,
// and also when it ends before it has the slot (ErrBusy, ctx, an endpoint
// that has ended). The duration is the time since the CALL took the slot,
// or, without it, the time it waited.
//
// req is read before Call returns and not retained: a json.RawMessage is
// copied into the frame while Call has the slot. A *json.RawMessage resp
// receives a copy of the payload.
func (e *Endpoint) Call(ctx context.Context, action string, req, resp any) error {
	if e.draining.Load() {
		return ocpp.ErrClosed
	}
	if e.cfg.CanSend != nil && !e.cfg.CanSend(action) {
		return fmt.Errorf("%w: %s from the %s", ocpp.ErrWrongDirection, action, e.cfg.Info.Role)
	}
	payload, err := marshalPayload(req)
	if err != nil {
		return err
	}
	info := ocpp.CallInfo{ConnInfo: e.cfg.Info, Action: action, MessageID: e.cfg.NewMessageID(), Direction: ocpp.Outgoing}
	run := func(ctx context.Context) error {
		start := time.Now()
		result, sent, err := e.roundTrip(ctx, info, payload)
		if err == nil {
			if err = unmarshalPayload(result, resp, action); err != nil {
				e.refuseResult(info.MessageID, err)
			}
		}
		if sent.IsZero() {
			sent = start // it ended before it had the slot
		}
		e.callDone(info, time.Since(sent), err)
		return err
	}
	if e.cfg.CallInterceptor != nil {
		return e.cfg.CallInterceptor(ctx, info, run)
	}
	return run(ctx)
}

// callDone reports the end of an outgoing CALL, with the error its Call
// returns, to the metrics and the log.
func (e *Endpoint) callDone(info ocpp.CallInfo, d time.Duration, err error) {
	e.cfg.Metrics.OnCallDone(e.cfg.Info, info.Action, d, err)
	if err != nil {
		e.log.Debug("call failed", "action", info.Action, "message_id", info.MessageID, "direction", "outgoing", "duration", d, "error", err)
		return
	}
	e.log.Debug("call done", "action", info.Action, "message_id", info.MessageID, "direction", "outgoing", "duration", d)
}

// refuseResult answers a CALLRESULT that cannot be processed with a
// CALLRESULTERROR (2.1 Part 4 §4.2.3), on OCPP 2.1 connections. Earlier
// versions have no such message: the error is returned to the caller only.
func (e *Endpoint) refuseResult(messageID string, err error) {
	if e.cfg.Info.Version != ocpp.V21 {
		return
	}
	var code ocpp.ErrorCode
	var desc string
	if ve, ok := errors.AsType[*ocpp.ValidationError](err); ok {
		ce := ve.CallError()
		code, desc = ce.Code, ce.Description
	} else {
		// Any other decoding error is classified as those of incoming CALL
		// payloads are (internal/wire Classify): a syntax error is a format
		// violation, anything else a value that does not fit its field. Its
		// text names Go types and stays in the local log.
		code, desc = ocpp.TypeConstraintViolation, "the response payload does not match its schema"
		if _, ok := errors.AsType[*json.SyntaxError](err); ok {
			code = ocppj.FormatErrorCode(e.cfg.Info.Version)
		}
	}
	e.log.Warn("response refused with CALLRESULTERROR", "message_id", messageID, "error_code", string(code), "error", err)
	e.sendResult(ocppj.Frame{Type: ocppj.CallResultError, ID: messageID, ErrorCode: code, Description: e.limit(desc)})
}

// Send sends a SEND (OCPP 2.1): it is never answered and does not wait for
// the outgoing CALL slot (2.1 Part 4 §4.1.1, §4.2.4). It returns once the
// frame is queued for writing.
func (e *Endpoint) Send(ctx context.Context, action string, req any) (err error) {
	if e.draining.Load() {
		return ocpp.ErrClosed
	}
	if e.cfg.Info.Version != ocpp.V21 {
		return fmt.Errorf("%w: SEND exists in OCPP 2.1 only, this connection uses %s", ocpp.ErrUnsupportedVersion, e.cfg.Info.Version)
	}
	if e.cfg.CanSend != nil && !e.cfg.CanSend(action) {
		return fmt.Errorf("%w: %s from the %s", ocpp.ErrWrongDirection, action, e.cfg.Info.Role)
	}
	payload, err := marshalPayload(req)
	if err != nil {
		return err
	}
	info := ocpp.CallInfo{ConnInfo: e.cfg.Info, Action: action, MessageID: e.cfg.NewMessageID(), Direction: ocpp.Outgoing}
	start := time.Now()
	defer func() { e.cfg.Metrics.OnCallDone(e.cfg.Info, action, time.Since(start), err) }()
	run := func(ctx context.Context) error {
		if err := e.Err(); err != nil {
			return err
		}
		frame, err := ocppj.Encode(ocppj.Frame{Type: ocppj.Send, ID: info.MessageID, Action: action, Payload: payload})
		if err != nil {
			return err
		}
		e.unwritten.Add(1)
		select {
		case e.sends <- frame:
			e.log.Debug("SEND queued", "action", action, "message_id", info.MessageID, "direction", "outgoing")
			return nil
		case <-ctx.Done():
			e.unwritten.Add(-1)
			return ctx.Err()
		case <-e.ctx.Done():
			e.unwritten.Add(-1)
			return e.cause
		}
	}
	if e.cfg.CallInterceptor != nil {
		err = e.cfg.CallInterceptor(ctx, info, run)
	} else {
		err = run(ctx)
	}
	return err
}

// roundTrip sends the CALL and waits for its answer. It returns the payload
// of the CALLRESULT, and when the CALL took the outgoing slot: the zero time
// when it ended before that.
func (e *Endpoint) roundTrip(ctx context.Context, info ocpp.CallInfo, payload json.RawMessage) (json.RawMessage, time.Time, error) {
	var none time.Time
	if err := e.Err(); err != nil {
		return nil, none, err
	}
	if n := e.waiting.Add(1); n > int64(e.cfg.MaxQueuedCalls) {
		e.waiting.Add(-1)
		return nil, none, fmt.Errorf("%w: %d calls already waiting", ocpp.ErrBusy, n-1)
	}
	select {
	case e.slot <- struct{}{}:
		e.waiting.Add(-1)
	case <-ctx.Done():
		e.waiting.Add(-1)
		return nil, none, ctx.Err()
	case <-e.ctx.Done():
		e.waiting.Add(-1)
		return nil, none, e.cause
	}

	frame, err := ocppj.Encode(ocppj.Frame{Type: ocppj.Call, ID: info.MessageID, Action: info.Action, Payload: payload})
	if err != nil {
		<-e.slot
		return nil, none, err
	}
	// The response may arrive before this goroutine runs again after the
	// send, so p is complete before it is queued.
	p := &pendingCall{id: info.MessageID, action: info.Action, info: info, sent: time.Now(), ch: make(chan callResult, 1)}
	e.mu.Lock()
	e.pending = p
	e.mu.Unlock()

	e.unwritten.Add(1)
	select {
	case e.calls <- outCall{data: frame, p: p}:
	case <-ctx.Done():
		e.unwritten.Add(-1)
		e.finish(p, callResult{err: ctx.Err()})
		return nil, p.sent, ctx.Err()
	case <-e.ctx.Done():
		e.unwritten.Add(-1)
		e.finish(p, callResult{err: e.cause})
		return nil, p.sent, e.cause
	}
	e.mu.Lock()
	if e.pending == p {
		// The response timeout matches ocpp.ErrTimeout and, as every
		// deadline that passed, context.DeadlineExceeded.
		p.timer = time.AfterFunc(e.cfg.ResponseTimeout, func() {
			e.finish(p, callResult{err: fmt.Errorf("%w: no response to %s (message %s) within %s: %w",
				ocpp.ErrTimeout, p.action, p.id, e.cfg.ResponseTimeout, context.DeadlineExceeded)})
		})
	}
	e.mu.Unlock()

	select {
	case r := <-p.ch:
		return r.payload, p.sent, r.err
	case <-ctx.Done():
		// A CALL the writer has not taken yet is not sent, and frees the
		// slot; one already taken keeps it until its response or timeout.
		e.finishUntaken(p, callResult{err: ctx.Err()})
		return nil, p.sent, ctx.Err()
	case <-e.ctx.Done():
		// shutdown finishes the call that is pending when it runs; a call
		// that became pending after that, its frame taken by the buffered
		// calls channel the writer no longer drains, is finished here.
		// finish runs once, so p.ch holds whichever result came first.
		e.finish(p, callResult{err: e.cause})
		r := <-p.ch
		return r.payload, p.sent, r.err
	}
}

// take marks p's CALL as taken by the writer, unless p has already finished.
func (e *Endpoint) take(p *pendingCall) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != p {
		return false
	}
	p.taken = true
	return true
}

// finish completes p once: it frees the slot and delivers the result. Later
// calls for the same p do nothing. The Call that waits for p reports its end
// (callDone); a result nobody waits for any more, the answer to a CALL whose
// caller's context ended, is dropped here.
func (e *Endpoint) finish(p *pendingCall, r callResult) { e.complete(p, r, false) }

// finishUntaken finishes p like finish, but only while the writer has not
// taken its CALL.
func (e *Endpoint) finishUntaken(p *pendingCall, r callResult) { e.complete(p, r, true) }

func (e *Endpoint) complete(p *pendingCall, r callResult, untakenOnly bool) {
	e.mu.Lock()
	if e.pending != p || (untakenOnly && p.taken) {
		e.mu.Unlock()
		return
	}
	e.pending = nil
	if p.timer != nil {
		p.timer.Stop()
	}
	e.mu.Unlock()
	<-e.slot
	p.ch <- r
}

// respond delivers a CALLRESULT or CALLERROR to the pending CALL it answers.
func (e *Endpoint) respond(f ocppj.Frame) {
	e.mu.Lock()
	p := e.pending
	match := p != nil && p.id == f.ID
	e.mu.Unlock()
	if !match {
		err := fmt.Errorf("%w: %s for message %q, which is not pending", ocpp.ErrUnexpectedResponse, f.Type, f.ID)
		e.cfg.Metrics.OnProtocolError(e.cfg.Info, err)
		e.log.Warn("unexpected response dropped", "message_id", f.ID, "error", err)
		return
	}
	if f.Type == ocppj.CallError {
		e.finish(p, callResult{err: &ocpp.CallError{Code: f.ErrorCode, Description: f.Description, Details: f.Details}})
		return
	}
	e.finish(p, callResult{payload: f.Payload})
}

func marshalPayload(req any) (json.RawMessage, error) {
	switch r := req.(type) {
	case nil:
		return json.RawMessage("{}"), nil
	case json.RawMessage:
		if len(r) == 0 {
			return json.RawMessage("{}"), nil
		}
		return r, nil
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ocpp: marshal request: %w", err)
	}
	return b, nil
}

func unmarshalPayload(payload json.RawMessage, resp any, action string) error {
	switch r := resp.(type) {
	case nil:
		return nil
	case *json.RawMessage:
		*r = append((*r)[:0], payload...)
		return nil
	}
	if err := json.Unmarshal(payload, resp); err != nil {
		return fmt.Errorf("%w: %s response: %w", ocpp.ErrProtocol, action, err)
	}
	// A typed response validates itself; validating here lets the engine
	// answer an invalid one with a CALLRESULTERROR (2.1).
	if v, ok := resp.(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("%w: %s response: %w", ocpp.ErrProtocol, action, err)
		}
	}
	return nil
}
