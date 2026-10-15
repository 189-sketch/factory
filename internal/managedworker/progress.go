package managedworker

import (
	"context"
	"sync/atomic"
)

type progressKey struct{}

func withRunProgress(ctx context.Context) context.Context {
	phase := &atomic.Value{}
	phase.Store("preparing")
	return context.WithValue(ctx, progressKey{}, phase)
}

func setRunProgress(ctx context.Context, value string) {
	if phase, ok := ctx.Value(progressKey{}).(*atomic.Value); ok {
		phase.Store(value)
	}
}

func runProgress(ctx context.Context) string {
	if phase, ok := ctx.Value(progressKey{}).(*atomic.Value); ok {
		return phase.Load().(string)
	}
	return ""
}
