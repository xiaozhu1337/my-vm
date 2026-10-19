//go:build linux

package guestagent

import (
	"context"
	"io"
	"my-vm/internal/sharing"
)

func Run(ctx context.Context, cancel context.CancelFunc, log io.Writer, mapShares func([]sharing.Share) error, desktop func(context.Context, io.Writer), _ string) error {
	return services(ctx, cancel, log, mapShares, desktop, func() error { return nil }, func(string) {})
}
