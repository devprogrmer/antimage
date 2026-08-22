package chaos

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ClockSkew represents clock skew between components
type ClockSkew struct {
	mu     sync.Mutex
	offset time.Duration
}

// NewClockSkew creates a clock skew simulator
func NewClockSkew(offset time.Duration) *ClockSkew {
	return &ClockSkew{offset: offset}
}

// Now returns the current time with skew applied
func (c *ClockSkew) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

// SetOffset changes the clock skew
func (c *ClockSkew) SetOffset(offset time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset = offset
}

// InjectClockSkew injects clock skew
func (i *Injector) InjectClockSkew(skew time.Duration) (*Fault, error) {
	fault := &Fault{
		ID:          fmt.Sprintf("clock-skew-%d", time.Now().UnixNano()),
		Type:        FaultTypeTiming,
		Description: fmt.Sprintf("Clock skew: %v", skew),
		InjectedAt:  time.Now(),
		RemoveFunc:  func() error { return nil },
	}

	if err := i.InjectFault(context.Background(), *fault); err != nil {
		return nil, err
	}

	return fault, nil
}

// InjectProcessingDelay injects artificial processing delay
func (i *Injector) InjectProcessingDelay(delay time.Duration) (*Fault, error) {
	fault := &Fault{
		ID:          fmt.Sprintf("proc-delay-%d", time.Now().UnixNano()),
		Type:        FaultTypeTiming,
		Description: fmt.Sprintf("Processing delay: %v", delay),
		InjectedAt:  time.Now(),
		RemoveFunc:  func() error { return nil },
	}

	if err := i.InjectFault(context.Background(), *fault); err != nil {
		return nil, err
	}

	return fault, nil
}

// DelayedContext wraps a context with artificial delay
type DelayedContext struct {
	context.Context
	delay time.Duration
}

// Deadline returns the deadline with delay applied
func (d *DelayedContext) Deadline() (time.Time, bool) {
	deadline, ok := d.Context.Deadline()
	if !ok {
		return time.Time{}, false
	}
	return deadline.Add(-d.delay), true
}

// NewDelayedContext creates a context that appears to have less time
func NewDelayedContext(ctx context.Context, delay time.Duration) context.Context {
	return &DelayedContext{
		Context: ctx,
		delay:   delay,
	}
}

// EventReorderer simulates out-of-order event delivery
type EventReorderer struct {
	mu      sync.Mutex
	queue   []interface{}
	maxSize int
}

// NewEventReorderer creates an event reorderer
func NewEventReorderer(maxSize int) *EventReorderer {
	return &EventReorderer{
		queue:   make([]interface{}, 0, maxSize),
		maxSize: maxSize,
	}
}

// Enqueue adds an event to the reorder queue
func (e *EventReorderer) Enqueue(event interface{}) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.queue = append(e.queue, event)
	if len(e.queue) > e.maxSize {
		// Dequeue oldest when full
		e.queue = e.queue[1:]
	}
}

// Dequeue retrieves events in potentially reordered fashion
func (e *EventReorderer) Dequeue() (interface{}, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.queue) == 0 {
		return nil, false
	}

	// Simulate reordering by sometimes taking from middle instead of front
	if len(e.queue) > 1 && time.Now().UnixNano()%3 == 0 {
		// Take from middle
		idx := len(e.queue) / 2
		event := e.queue[idx]
		e.queue = append(e.queue[:idx], e.queue[idx+1:]...)
		return event, true
	}

	// Normal FIFO
	event := e.queue[0]
	e.queue = e.queue[1:]
	return event, true
}

// DuplicateEventInjector simulates duplicate event delivery
type DuplicateEventInjector struct {
	mu            sync.Mutex
	duplicateRate float64 // 0.0-1.0, probability of duplicating an event
}

// NewDuplicateEventInjector creates a duplicate event injector
func NewDuplicateEventInjector(duplicateRate float64) *DuplicateEventInjector {
	return &DuplicateEventInjector{
		duplicateRate: duplicateRate,
	}
}

// Process processes an event, potentially duplicating it
func (d *DuplicateEventInjector) Process(event interface{}) []interface{} {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Simple probabilistic duplication
	if float64(time.Now().UnixNano()%100)/100.0 < d.duplicateRate {
		return []interface{}{event, event} // Duplicate
	}

	return []interface{}{event} // Single
}
