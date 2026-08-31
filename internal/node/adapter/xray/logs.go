package xray

import (
	"context"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// defaultLogLines and maxLogLines bound ReadLogs: a caller that asks for
// zero or a negative count gets a screenful instead of an error, and one
// that asks for everything journald has ever recorded is clamped rather
// than allowed to pull an unbounded amount of text back over the command
// channel that also carries every other on-demand command this node
// handles.
const (
	defaultLogLines = 200
	maxLogLines     = 2000
)

var _ adapter.LogReader = (*Adapter)(nil)

// ReadLogs implements adapter.LogReader.
func (a *Adapter) ReadLogs(ctx context.Context, lines int) (string, error) {
	if lines <= 0 {
		lines = defaultLogLines
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}
	return a.rt.ReadLog(ctx, lines)
}
