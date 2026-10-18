// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/transport"
	"github.com/otfabric/go-ocpp/ocppj"
)

func (e *Endpoint) readLoop() {
	for {
		data, err := e.conn.ReadMessage(e.ctx)
		if err != nil {
			var ce *transport.CloseError
			if errors.As(err, &ce) {
				e.shutdown(lost(err), ce.Code, "")
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
		case ocppj.Call:
			select {
			case e.incoming <- f:
			case <-e.ctx.Done():
				return
			}
		case ocppj.CallResult, ocppj.CallError:
			e.respond(f)
		default:
			// CALLRESULTERROR and SEND (OCPP 2.1) are handled from Phase 6.
			err := fmt.Errorf("%w: %s frames are not supported yet", ocpp.ErrProtocol, f.Type)
			e.cfg.Metrics.OnProtocolError(e.cfg.Info, err)
			e.log.Warn("frame dropped", "message_id", f.ID, "error", err)
		}
	}
}

// malformed handles a frame that does not decode. A CALL whose message ID
// could be read is answered with a CALLERROR. Anything else is dropped,
// including frames of an unknown message type (SPECIFICATIONS.md T9),
// because there is nothing a response could be correlated with.
func (e *Endpoint) malformed(err error) {
	err = fmt.Errorf("%w: %w", ocpp.ErrProtocol, err)
	e.cfg.Metrics.OnProtocolError(e.cfg.Info, err)
	var de *ocppj.DecodeError
	if errors.As(err, &de) && de.Type == ocppj.Call && de.IDKnown && !de.UnknownType {
		e.log.Warn("malformed CALL answered with CALLERROR", "message_id", de.ID, "error", err)
		e.sendResult(ocppj.Frame{Type: ocppj.CallError, ID: de.ID,
			ErrorCode: ocppj.RPCErrorCode(e.cfg.Info.Version), Description: de.Reason})
		return
	}
	e.log.Warn("malformed frame dropped", "error", err)
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
			case data = <-e.calls:
			case <-e.ctx.Done():
				return
			}
		}
		ctx, cancel := context.WithTimeout(e.ctx, e.cfg.WriteTimeout)
		err := e.conn.WriteMessage(ctx, data)
		cancel()
		if err != nil {
			e.shutdown(lost(fmt.Errorf("write: %w", err)), transport.StatusInternalError, "")
			return
		}
		if obs := e.cfg.FrameObserver; obs != nil {
			obs(e.cfg.Info, ocpp.Outgoing, data)
		}
	}
}

func (e *Endpoint) sendResult(f ocppj.Frame) {
	data, err := ocppj.Encode(f)
	if err != nil {
		e.log.Error("cannot encode a response", "message_id", f.ID, "error", err)
		return
	}
	select {
	case e.results <- data:
	case <-e.ctx.Done():
	}
}

func (e *Endpoint) dispatchLoop() {
	for {
		select {
		case f := <-e.incoming:
			e.inHandler.Store(true)
			e.handle(f)
			e.inHandler.Store(false)
		case <-e.ctx.Done():
			return
		}
	}
}

// handle runs the dispatcher for one incoming CALL and sends its answer.
func (e *Endpoint) handle(f ocppj.Frame) {
	start := time.Now()
	info := ocpp.CallInfo{ConnInfo: e.cfg.Info, Action: f.Action, MessageID: f.ID, Direction: ocpp.Incoming}
	var result json.RawMessage
	run := func(ctx context.Context) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("handler panic: %v", r)
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
		err = e.cfg.HandlerInterceptor(e.ctx, info, run)
	} else {
		err = run(e.ctx)
	}
	if err == nil && !isObject(result) {
		err = errors.New("handler returned no response object")
	}
	e.cfg.Metrics.OnCallHandled(e.cfg.Info, f.Action, time.Since(start), err)
	if err == nil {
		e.log.Debug("call handled", "action", f.Action, "message_id", f.ID, "direction", "incoming", "duration", time.Since(start))
		e.sendResult(ocppj.Frame{Type: ocppj.CallResult, ID: f.ID, Payload: result})
		return
	}
	e.sendResult(ocppj.Frame{Type: ocppj.CallError, ID: f.ID, ErrorCode: e.errorCode(err, f), Description: e.description(err)})
}

// errorCode chooses the CALLERROR code for a handler error. Only a
// *ocpp.CallError with a code the version defines is sent as is.
func (e *Endpoint) errorCode(err error, f ocppj.Frame) ocpp.ErrorCode {
	var ce *ocpp.CallError
	if errors.As(err, &ce) {
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
