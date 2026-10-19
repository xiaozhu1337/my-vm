package guestagent

import (
	"context"
	"fmt"
	"io"
	"sync"

	"my-vm/internal/guestops"
	"my-vm/internal/sharing"
)

func services(ctx context.Context, cancel context.CancelFunc, log io.Writer, mapShares func([]sharing.Share) error, desktop func(context.Context, io.Writer), status guestops.StatusProvider, ready func() error, update func(string)) error {
	startup := make(chan error, 2)
	results := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() { desktop(ctx, log) })
	workers.Go(func() { guestops.RunStatus(ctx, log, func(err error) { startup <- err }, status) })
	workers.Go(func() {
		results <- sharing.RunProxyReady(ctx, log, mapShares, func(err error) { startup <- err }, func(err error) {
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				update("代理运行中 · 共享目录连接失败，请查看日志")
			} else {
				update("代理运行中 · 共享目录已就绪")
			}
		})
	})
	defer workers.Wait()
	defer cancel()
	for range 2 {
		select {
		case err := <-startup:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
	if err := ready(); err != nil {
		return err
	}
	select {
	case err := <-results:
		return err
	case <-ctx.Done():
		return nil
	}
}

func report(log io.Writer, err error) {
	if err != nil {
		fmt.Fprintln(log, err)
	}
}
