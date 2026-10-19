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
// timeout expires, as T6 requires.
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
		result, err := e.roundTrip(ctx, info, payload)
		if err != nil {
			return err
		}
		if err := unmarshalPayload(result, resp, action); err != nil {
			e.refuseResult(info.MessageID, err)
			return err
		}
		return nil
	}
	if e.cfg.CallInterceptor != nil {
		return e.cfg.CallInterceptor(ctx, info, run)
	}
	return run(ctx)
}

// refuseResult answers a CALLRESULT that cannot be processed with a
// CALLRESULTERROR (2.1 Part 4 §4.2.3), on OCPP 2.1 connections. Earlier
// versions have no such message: the error is returned to the caller only.
func (e *Endpoint) refuseResult(messageID string, err error) {
	if e.cfg.Info.Version != ocpp.V21 {
		return
	}
	code, desc := ocppj.FormatErrorCode(e.cfg.Info.Version), err.Error()
	var ve *ocpp.ValidationError
	if errors.As(err, &ve) {
		ce := ve.CallError()
		code, desc = ce.Code, ce.Description
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

func (e *Endpoint) roundTrip(ctx context.Context, info ocpp.CallInfo, payload json.RawMessage) (json.RawMessage, error) {
	if err := e.Err(); err != nil {
		return nil, err
	}
	if n := e.waiting.Add(1); n > int64(e.cfg.MaxQueuedCalls) {
		e.waiting.Add(-1)
		return nil, fmt.Errorf("%w: %d calls already waiting", ocpp.ErrBusy, n-1)
	}
	select {
	case e.slot <- struct{}{}:
		e.waiting.Add(-1)
	case <-ctx.Done():
		e.waiting.Add(-1)
		return nil, ctx.Err()
	case <-e.ctx.Done():
		e.waiting.Add(-1)
		return nil, e.cause
	}

	frame, err := ocppj.Encode(ocppj.Frame{Type: ocppj.Call, ID: info.MessageID, Action: info.Action, Payload: payload})
	if err != nil {
		<-e.slot
		return nil, err
	}
	// The response may arrive before this goroutine runs again after the
	// send, so p is complete before it is queued.
	p := &pendingCall{id: info.MessageID, action: info.Action, info: info, sent: time.Now(), ch: make(chan callResult, 1)}
	e.mu.Lock()
	e.pending = p
	e.mu.Unlock()

	e.unwritten.Add(1)
	select {
	case e.calls <- frame:
	case <-ctx.Done():
		e.unwritten.Add(-1)
		e.finish(p, callResult{err: ctx.Err()})
		return nil, ctx.Err()
	case <-e.ctx.Done():
		e.unwritten.Add(-1)
		e.finish(p, callResult{err: e.cause})
		return nil, e.cause
	}
	e.mu.Lock()
	if e.pending == p {
		p.timer = time.AfterFunc(e.cfg.ResponseTimeout, func() {
			e.finish(p, callResult{err: fmt.Errorf("%w: no response to %s (message %s) within %s",
				ocpp.ErrTimeout, p.action, p.id, e.cfg.ResponseTimeout)})
		})
	}
	e.mu.Unlock()

	select {
	case r := <-p.ch:
		return r.payload, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.ctx.Done():
		// shutdown finishes the call that is pending when it runs; a call
		// that became pending after that, its frame taken by the buffered
		// calls channel the writer no longer drains, is finished here.
		// finish runs once, so p.ch holds whichever result came first.
		e.finish(p, callResult{err: e.cause})
		r := <-p.ch
		return r.payload, r.err
	}
}

// finish completes p once: it frees the slot and delivers the result. Later
// calls for the same p do nothing.
func (e *Endpoint) finish(p *pendingCall, r callResult) {
	e.mu.Lock()
	if e.pending != p {
		e.mu.Unlock()
		return
	}
	e.pending = nil
	if p.timer != nil {
		p.timer.Stop()
	}
	sent := p.sent
	e.mu.Unlock()
	<-e.slot
	p.ch <- r
	d := time.Since(sent)
	e.cfg.Metrics.OnCallDone(e.cfg.Info, p.action, d, r.err)
	if r.err != nil {
		e.log.Debug("call failed", "action", p.action, "message_id", p.id, "direction", "outgoing", "duration", d, "error", r.err)
	} else {
		e.log.Debug("call done", "action", p.action, "message_id", p.id, "direction", "outgoing", "duration", d)
	}
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
