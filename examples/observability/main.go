// SPDX-License-Identifier: MIT

// Command observability shows the hooks go-ocpp offers for logs, metrics
// and tracing, with a CSMS and a Charging Station in one process: a JSON
// slog logger, an ocpp.Metrics implementation that counts, and call and
// handler interceptors that time every CALL (where a tracer would start
// and end its spans). Exporting to Prometheus or OpenTelemetry is the
// application's: the callbacks and interceptors are where it plugs in.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/server"
)

// counters is an ocpp.Metrics that counts events by name. A Prometheus
// implementation would increment counter vectors and observe histograms
// in the same callbacks.
type counters struct {
	ocpp.NopMetrics // keeps compiling when callbacks are added
	mu              sync.Mutex
	n               map[string]int
}

func (c *counters) inc(name string) {
	c.mu.Lock()
	c.n[name]++
	c.mu.Unlock()
}

func (c *counters) OnConnect(info ocpp.ConnInfo) { c.inc(info.Role.String() + " connect") }
func (c *counters) OnDisconnect(info ocpp.ConnInfo, err error) {
	c.inc(info.Role.String() + " disconnect")
}
func (c *counters) OnCallDone(info ocpp.ConnInfo, action string, d time.Duration, err error) {
	c.inc(fmt.Sprintf("%s sent %s ok=%v", info.Role, action, err == nil))
}
func (c *counters) OnCallHandled(info ocpp.ConnInfo, action string, d time.Duration, err error) {
	c.inc(fmt.Sprintf("%s handled %s ok=%v", info.Role, action, err == nil))
}

func (c *counters) print() {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.n))
	for k := range c.n {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Printf("metric %-50s %d\n", k, c.n[k])
	}
}

// timing is an interceptor that logs each CALL with its duration; a tracer
// would start a span before next and end it after.
func timing(l *slog.Logger) ocpp.Interceptor {
	return func(ctx context.Context, info ocpp.CallInfo, next func(context.Context) error) error {
		start := time.Now()
		err := next(ctx)
		l.Info("span", "station_id", info.StationID, "action", info.Action, "message_id", info.MessageID,
			"direction", info.Direction.String(), "duration", time.Since(start), "error", err)
		return err
	}
}

type csms struct {
	ocpp201.UnimplementedCSMSHandler
}

func (csms) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	metrics := &counters{n: map[string]int{}}

	srv, err := server.New(
		server.WithHandler(ocpp.V201, ocpp201.CSMS(csms{})),
		server.WithoutAuthentication(), // a closed example network; use WithAuthenticator in production
		server.WithLogger(logger),
		server.WithMetrics(metrics),
		server.WithHandlerInterceptor(timing(logger)),
		server.WithCallInterceptor(timing(logger)),
	)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(ctx) }()

	c, err := client.New("ws://"+ln.Addr().String()+"/ocpp/OBSERVED-1",
		client.WithInsecureTransport(),
		client.WithHandler(ocpp.V201, ocpp201.ChargingStation(ocpp201.UnimplementedChargingStationHandler{})),
		client.WithLogger(logger),
		client.WithMetrics(metrics),
		client.WithCallInterceptor(timing(logger)),
		client.WithFrameObserver(func(info ocpp.ConnInfo, dir ocpp.Direction, frame []byte) {
			// Raw frames, for a protocol trace; payloads can hold personal
			// data, so a real application decides what to keep.
			logger.Debug("frame", "direction", dir.String(), "bytes", len(frame), "valid_json", json.Valid(frame))
		}),
	)
	if err != nil {
		return err
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}
	if _, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{
		Reason:          ocpp201.BootReasonPowerUp,
		ChargingStation: ocpp201.ChargingStationType{VendorName: "OT Fabric", Model: "TestStation"},
	}); err != nil {
		return err
	}
	// Not handled by this CSMS: answered NotSupported, counted as failed.
	_, _ = ocpp201.Heartbeat(ctx, c, &ocpp201.HeartbeatRequest{})
	session, ok := srv.Station("OBSERVED-1")
	if !ok {
		return fmt.Errorf("no session")
	}
	// Not handled by this station: NotSupported.
	_, _ = ocpp201.Reset(ctx, session, &ocpp201.ResetRequest{Type: ocpp201.ResetImmediate})
	_ = c.Close()
	<-session.Done()
	metrics.print()
	return nil
}
