// Package agent implements the node-side control-plane client.
//
// accounting.go implements SP3's accounting loop: polls adapters that declare
// SelfAccounting, computes deltas, persists them, and ships usage reports to
// the panel.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
	antimagev1 "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

// AccountingState persists deltas and sequence numbers between polls.
type AccountingState struct {
	Sequence int64                   `json:"sequence"`
	Pending  []*antimagev1.UsageSample `json:"pending"`
}

// AccountingLoop polls adapters for usage deltas and sends reports to the panel.
// It runs until ctx is cancelled. Interval controls poll frequency (design:
// "deliberately short" to bound loss on restart).
//
// This must be called from within a session where the stream is available.
func (c *Client) AccountingLoop(ctx context.Context, stream pb.Control_StreamClient, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second // Default: 30s poll interval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.pollAndReport(ctx, stream); err != nil {
				slog.WarnContext(ctx, "accounting poll failed", "error", err)
			}
		}
	}
}

func (c *Client) pollAndReport(ctx context.Context, stream pb.Control_StreamClient) error {
	// Check if the adapter supports accounting.
	reporter, ok := c.ad.(adapter.UsageReporter)
	if !ok {
		// Adapter doesn't support self-accounting.
		return nil
	}

	samples, err := reporter.Usage(ctx)
	if err != nil {
		return fmt.Errorf("adapter usage query failed: %w", err)
	}

	if len(samples) == 0 {
		// No traffic since last poll.
		return nil
	}

	// Load accounting state (sequence + pending deltas).
	state, err := c.loadAccountingState()
	if err != nil {
		return fmt.Errorf("load accounting state: %w", err)
	}

	// Convert to protobuf samples.
	protoSamples := make([]*antimagev1.UsageSample, len(samples))
	for i, s := range samples {
		protoSamples[i] = &antimagev1.UsageSample{
			SubjectId:     s.SubjectID,
			UplinkBytes:   s.UplinkBytes,
			DownlinkBytes: s.DownlinkBytes,
		}
	}

	// Add new samples to pending.
	state.Pending = append(state.Pending, protoSamples...)
	state.Sequence++

	// Persist before sending (durability: deltas survive agent restart).
	if err := c.saveAccountingState(state); err != nil {
		return fmt.Errorf("save accounting state: %w", err)
	}

	// Send usage report to panel.
	report := &antimagev1.UsageReport{
		NodeId:   c.cfg.NodeID,
		Sequence: state.Sequence,
		Samples:  state.Pending,
	}

	msg := &antimagev1.AgentMessage{
		Payload: &antimagev1.AgentMessage_UsageReport{
			UsageReport: report,
		},
	}

	if err := stream.Send(msg); err != nil {
		return fmt.Errorf("send usage report: %w", err)
	}

	// On successful send, clear pending (panel ACK is implicit: if send succeeds,
	// the panel received it). A retry after network error will re-send with the
	// same sequence, and the panel's idempotency key deduplicates it.
	state.Pending = nil
	if err := c.saveAccountingState(state); err != nil {
		// Non-fatal: worst case we re-send deltas that the panel already applied,
		// and idempotency rejects them.
		slog.WarnContext(ctx, "failed to clear pending deltas after send", "error", err)
	}

	slog.InfoContext(ctx, "usage report sent",
		"sequence", state.Sequence, "samples", len(report.Samples))
	return nil
}

func (c *Client) accountingStatePath() string {
	return filepath.Join(c.cfg.StateDir, "accounting.json")
}

func (c *Client) loadAccountingState() (*AccountingState, error) {
	path := c.accountingStatePath()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// First run: start at sequence 1.
		return &AccountingState{Sequence: 0}, nil
	}
	if err != nil {
		return nil, err
	}
	var state AccountingState
	if err := json.Unmarshal(raw, &state); err != nil {
		// Corrupted state: start fresh rather than fail.
		return &AccountingState{Sequence: 0}, nil
	}
	return &state, nil
}

func (c *Client) saveAccountingState(state *AccountingState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(c.accountingStatePath(), raw, 0o600)
}
