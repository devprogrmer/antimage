package chaos

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

// NetworkFaultConfig configures network fault injection
type NetworkFaultConfig struct {
	// Timeout injects a timeout on network operations
	Timeout time.Duration
	// Latency adds artificial latency to operations
	Latency time.Duration
	// PacketLoss drops packets at specified rate (0.0-1.0)
	PacketLoss float64
	// Partition simulates network partition (complete disconnect)
	Partition bool
}

// InjectNetworkTimeout injects a network timeout fault
func (i *Injector) InjectNetworkTimeout(timeout time.Duration) (*Fault, error) {
	fault := &Fault{
		ID:          fmt.Sprintf("net-timeout-%d", time.Now().UnixNano()),
		Type:        FaultTypeNetwork,
		Description: fmt.Sprintf("Network timeout: %v", timeout),
		InjectedAt:  time.Now(),
		RemoveFunc:  func() error { return nil },
	}

	if err := i.InjectFault(context.Background(), *fault); err != nil {
		return nil, err
	}

	return fault, nil
}

// InjectNetworkLatency adds artificial network latency
func (i *Injector) InjectNetworkLatency(latency time.Duration) (*Fault, error) {
	fault := &Fault{
		ID:          fmt.Sprintf("net-latency-%d", time.Now().UnixNano()),
		Type:        FaultTypeNetwork,
		Description: fmt.Sprintf("Network latency: %v", latency),
		InjectedAt:  time.Now(),
		RemoveFunc:  func() error { return nil },
	}

	if err := i.InjectFault(context.Background(), *fault); err != nil {
		return nil, err
	}

	return fault, nil
}

// InjectNetworkPartition simulates complete network partition
func (i *Injector) InjectNetworkPartition() (*Fault, error) {
	fault := &Fault{
		ID:          fmt.Sprintf("net-partition-%d", time.Now().UnixNano()),
		Type:        FaultTypeNetwork,
		Description: "Network partition (complete disconnect)",
		InjectedAt:  time.Now(),
		RemoveFunc:  func() error { return nil },
	}

	if err := i.InjectFault(context.Background(), *fault); err != nil {
		return nil, err
	}

	return fault, nil
}

// GRPCFaultInjector wraps a gRPC connection with fault injection
type GRPCFaultInjector struct {
	// faultActive and faultType were declared but never set or read.
	conn          *grpc.ClientConn
	originalState connectivity.State
}

// NewGRPCFaultInjector creates a gRPC fault injector for a connection
func NewGRPCFaultInjector(conn *grpc.ClientConn) *GRPCFaultInjector {
	return &GRPCFaultInjector{
		conn:          conn,
		originalState: conn.GetState(),
	}
}

// InjectGRPCConnectionDrop simulates gRPC connection drop
func (i *Injector) InjectGRPCConnectionDrop(conn *grpc.ClientConn) (*Fault, error) {
	fault := &Fault{
		ID:          fmt.Sprintf("grpc-drop-%d", time.Now().UnixNano()),
		Type:        FaultTypeGRPC,
		Description: "gRPC connection drop",
		InjectedAt:  time.Now(),
		RemoveFunc: func() error {
			// In real implementation, would restore connection
			return nil
		},
	}

	if err := i.InjectFault(context.Background(), *fault); err != nil {
		return nil, err
	}

	// Simulate connection drop by closing the connection
	// In actual test, this would be more controlled
	if conn != nil {
		_ = conn.Close()
	}

	return fault, nil
}

// InjectGRPCTimeout injects gRPC call timeout
func (i *Injector) InjectGRPCTimeout(timeout time.Duration) (*Fault, error) {
	fault := &Fault{
		ID:          fmt.Sprintf("grpc-timeout-%d", time.Now().UnixNano()),
		Type:        FaultTypeGRPC,
		Description: fmt.Sprintf("gRPC timeout: %v", timeout),
		InjectedAt:  time.Now(),
		RemoveFunc:  func() error { return nil },
	}

	if err := i.InjectFault(context.Background(), *fault); err != nil {
		return nil, err
	}

	return fault, nil
}

// FaultyDialer creates a net.Dialer that injects network faults
type FaultyDialer struct {
	mu      sync.Mutex
	base    *net.Dialer
	latency time.Duration
	timeout time.Duration
	fail    bool
}

// NewFaultyDialer creates a dialer with fault injection
func NewFaultyDialer(latency, timeout time.Duration, fail bool) *FaultyDialer {
	return &FaultyDialer{
		base:    &net.Dialer{Timeout: 30 * time.Second},
		latency: latency,
		timeout: timeout,
		fail:    fail,
	}
}

// Dial implements net.Dialer with fault injection
func (d *FaultyDialer) Dial(network, address string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.fail {
		return nil, fmt.Errorf("injected dial failure")
	}

	if d.latency > 0 {
		time.Sleep(d.latency)
	}

	if d.timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
		defer cancel()
		return d.base.DialContext(ctx, network, address)
	}

	return d.base.Dial(network, address)
}

// DialContext implements context-aware dialing with faults
func (d *FaultyDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.fail {
		return nil, fmt.Errorf("injected dial failure")
	}

	if d.latency > 0 {
		select {
		case <-time.After(d.latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if d.timeout > 0 {
		timeoutCtx, cancel := context.WithTimeout(ctx, d.timeout)
		defer cancel()
		return d.base.DialContext(timeoutCtx, network, address)
	}

	return d.base.DialContext(ctx, network, address)
}
