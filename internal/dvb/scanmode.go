package dvb

import (
	"context"
	"errors"
	"time"
)

// ErrNoSignal means the tuner saw no signal or carrier at all on a channel.
var ErrNoSignal = errors.New("no signal on this channel")

// QuickScanGiveUp is how long a scan waits for any sign of a signal.
var QuickScanGiveUp = 6 * time.Second

type quickScanKey struct{}

// WithQuickScan marks a tuning context as a channel scan, which gives up
// early on channels with no signal.
func WithQuickScan(ctx context.Context) context.Context {
	return context.WithValue(ctx, quickScanKey{}, true)
}

// IsQuickScan reports whether ctx was marked with WithQuickScan.
func IsQuickScan(ctx context.Context) bool {
	v, _ := ctx.Value(quickScanKey{}).(bool)
	return v
}
