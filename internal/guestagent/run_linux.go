//go:build linux

package guestagent

import (
	"context"
	"io"
	"my-vm/internal/guestops"
	"my-vm/internal/sharing"
)

func Run(ctx context.Context, cancel context.CancelFunc, log io.Writer, mapShares func([]sharing.Share) error, desktop func(context.Context, io.Writer), status guestops.StatusProvider, _ string) error {
	return services(ctx, cancel, log, mapShares, desktop, status, func() error { return nil }, func(string) {})
}
