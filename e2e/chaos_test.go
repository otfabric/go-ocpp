// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// Chaos runs for OCPP_CHAOS_DURATION (default 2s) with the seed
// OCPP_CHAOS_SEED (default: random, printed), so that a failure can be
// replayed. make chaos and make soak set them.
func chaosConfig(t *testing.T) (time.Duration, uint64) {
	t.Helper()
	d := 2 * time.Second
	if s := os.Getenv("OCPP_CHAOS_DURATION"); s != "" {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			t.Fatalf("OCPP_CHAOS_DURATION: %v", err)
		}
	}
	seed := rand.Uint64()
	if s := os.Getenv("OCPP_CHAOS_SEED"); s != "" {
		var err error
		if seed, err = strconv.ParseUint(s, 10, 64); err != nil {
			t.Fatalf("OCPP_CHAOS_SEED: %v", err)
		}
	}
	t.Logf("chaos for %s with OCPP_CHAOS_SEED=%d", d, seed)
	return d, seed
}

// chaosProxy forwards TCP connections to a target and disturbs them: it
// splits the byte stream into random chunks, delays them, and cuts a
// connection after a random number of bytes. Heal stops the disturbance
// for new and existing connections.
type chaosProxy struct {
	ln     net.Listener
	target string

	mu    sync.Mutex
	rng   *rand.Rand
	conns map[net.Conn]struct{}

	healed  atomic.Bool
	frozen  atomic.Bool // forwards nothing, closes nothing: a half-open link
	closing atomic.Bool
	cuts    atomic.Int64
	wg      sync.WaitGroup
}

func newChaosProxy(t *testing.T, target string, seed uint64) *chaosProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &chaosProxy{ln: ln, target: target, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), conns: map[net.Conn]struct{}{}}
	p.wg.Add(1)
	go p.accept()
	t.Cleanup(p.close)
	return p
}

func (p *chaosProxy) intN(n int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rng.IntN(n)
}

func (p *chaosProxy) accept() {
	defer p.wg.Done()
	for {
		in, err := p.ln.Accept()
		if err != nil {
			return
		}
		out, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = in.Close()
			continue
		}
		p.mu.Lock()
		p.conns[in], p.conns[out] = struct{}{}, struct{}{}
		p.mu.Unlock()
		// Each connection may carry 1–64 KiB before it is cut.
		budget := &atomic.Int64{}
		budget.Store(int64(1024 + p.intN(63*1024)))
		cut := sync.OnceFunc(func() {
			p.cuts.Add(1)
			_ = in.Close()
			_ = out.Close()
		})
		p.wg.Add(2)
		go p.pipe(in, out, budget, cut)
		go p.pipe(out, in, budget, cut)
	}
}

func (p *chaosProxy) pipe(from, to net.Conn, budget *atomic.Int64, cut func()) {
	defer p.wg.Done()
	defer func() {
		_ = from.Close()
		_ = to.Close()
		p.mu.Lock()
		delete(p.conns, from)
		delete(p.conns, to)
		p.mu.Unlock()
	}()
	buf := make([]byte, 32*1024)
	for {
		n, err := from.Read(buf)
		data := buf[:n]
		for p.frozen.Load() && !p.closing.Load() {
			time.Sleep(time.Millisecond)
		}
		for len(data) > 0 {
			chunk := len(data)
			if !p.healed.Load() {
				if budget.Add(-int64(chunk)) < 0 {
					cut()
					return
				}
				chunk = 1 + p.intN(len(data))
				if p.intN(4) == 0 {
					time.Sleep(time.Duration(p.intN(2000)) * time.Microsecond)
				}
			}
			if _, werr := to.Write(data[:chunk]); werr != nil {
				return
			}
			data = data[chunk:]
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = to.Close()
			}
			return
		}
	}
}

// cutAll cuts every open connection.
func (p *chaosProxy) cutAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
	}
}

func (p *chaosProxy) close() {
	p.closing.Store(true)
	_ = p.ln.Close()
	p.cutAll()
	p.wg.Wait()
}

// TestChaos runs stations of every version through a chaosProxy that
// fragments, delays and cuts their connections, while both sides call and
// the 2.1 stations SEND. Throughout, every call returns either the answer
// to its own request (never another's: the echo carries the station, the
// version and a nonce) or an error a broken connection explains. After the
// proxy heals, every station reconnects and a call in each direction
// succeeds. Nothing leaks.
func TestChaos(t *testing.T) {
	noLeak(t)
	duration, seed := chaosConfig(t)
	var sends atomic.Int64
	d := &sendRecorder{echo: ocpp.DispatcherFunc(echo), onSend: func(json.RawMessage) { sends.Add(1) }}
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, d), server.WithHandler(ocpp.V201, d), server.WithHandler(ocpp.V21, d),
		server.WithoutAuthentication(), server.WithResponseTimeout(2*time.Second))
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newChaosProxy(t, u.Host, seed)
	proxied := "ws://" + proxy.ln.Addr().String() + u.Path

	const stations = 12
	versions := []ocpp.Version{ocpp.V16, ocpp.V201, ocpp.V21}
	type st struct {
		id string
		v  ocpp.Version
		c  *client.Client
	}
	var all []st
	for i := range stations {
		v := versions[i%3]
		id := fmt.Sprintf("CH%02d", i)
		opts := []client.Option{client.WithInsecureTransport(), client.WithVersions(v), client.WithHandler(v, ocpp.DispatcherFunc(echo)),
			client.WithResponseTimeout(2 * time.Second), client.WithHandshakeTimeout(2 * time.Second),
			client.WithReconnect(client.Reconnect{WaitMinimum: 10 * time.Millisecond, RandomRange: 20 * time.Millisecond, RepeatTimes: 2})}
		if v != ocpp.V16 && i%2 == 0 {
			opts = append(opts, client.WithCompression())
		}
		c := station(t, proxied+id, opts...)
		// The first connect may be cut too; retry until it holds.
		eventually(t, id+" connected", func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := c.Connect(ctx)
			return err == nil || errors.Is(err, ocpp.ErrInvalidOption)
		})
		all = append(all, st{id, v, c})
	}

	var ok, failed atomic.Int64
	check := func(who, id string, v ocpp.Version, nonce string, got echoed, err error) {
		if err != nil {
			if !expectedFailure(err) {
				t.Errorf("%s %s: unexpected error: %v", who, id, err)
			}
			failed.Add(1)
			return
		}
		if got.Station != id || got.Version != v.String() || string(got.Echo) != nonce {
			t.Errorf("%s %s: sent %s on %s, answered %+v", who, id, nonce, v, got)
		}
		ok.Add(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(2)
		go func() { // the station calls, and SENDs on 2.1
			defer wg.Done()
			for i := 0; ctx.Err() == nil; i++ {
				nonce := fmt.Sprintf(`{"n":%d}`, i)
				var got echoed
				err := s.c.Call(ctx, "DataTransfer", json.RawMessage(nonce), &got)
				check("station", s.id, s.v, nonce, got, err)
				if s.v == ocpp.V21 {
					if err := s.c.Send(ctx, "NotifyPeriodicEventStream", json.RawMessage(nonce)); err != nil && !expectedFailure(err) {
						t.Errorf("SEND %s: %v", s.id, err)
					}
				}
				if errors.Is(err, ocpp.ErrNotConnected) {
					time.Sleep(5 * time.Millisecond)
				}
			}
		}()
		go func() { // the CSMS calls the station's current session
			defer wg.Done()
			for i := 0; ctx.Err() == nil; i++ {
				sess, found := srv.Station(s.id)
				if !found {
					time.Sleep(5 * time.Millisecond)
					continue
				}
				nonce := fmt.Sprintf(`{"m":%d}`, i)
				var got echoed
				err := sess.Call(ctx, "DataTransfer", json.RawMessage(nonce), &got)
				check("csms", s.id, s.v, nonce, got, err)
			}
		}()
	}
	// Besides the byte budgets, cut everything now and then.
	go func() {
		for ctx.Err() == nil {
			time.Sleep(time.Duration(100+proxy.intN(300)) * time.Millisecond)
			proxy.cutAll()
		}
	}()
	wg.Wait()
	t.Logf("%d calls answered, %d failed, %d connections cut, %d SENDs received", ok.Load(), failed.Load(), proxy.cuts.Load(), sends.Load())

	// Heal: every station reconnects, and both directions work again.
	proxy.healed.Store(true)
	for _, s := range all {
		eventually(t, s.id+" calls again", func() bool {
			cctx, ccancel := context.WithTimeout(context.Background(), time.Second)
			defer ccancel()
			return s.c.Call(cctx, "Heartbeat", nil, nil) == nil
		})
		eventually(t, "the CSMS calls "+s.id+" again", func() bool {
			sess, found := srv.Station(s.id)
			if !found {
				return false
			}
			cctx, ccancel := context.WithTimeout(context.Background(), time.Second)
			defer ccancel()
			return sess.Call(cctx, "Heartbeat", nil, nil) == nil
		})
	}
	if ok.Load() == 0 {
		t.Fatal("no call succeeded under chaos")
	}
}
