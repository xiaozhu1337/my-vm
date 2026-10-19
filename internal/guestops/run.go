package guestops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

func Run(ctx context.Context, log io.Writer) {
	listener, err := listen()
	if err != nil {
		fmt.Fprintln(log, "客户机命令服务:", err)
		return
	}
	defer listener.Close()
	slots := make(chan struct{}, 8)
	handler := Handler()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second,
		MaxHeaderBytes: 16 << 10, BaseContext: func(_ net.Listener) context.Context { return ctx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				http.Error(w, "guest operation limit reached", 503)
				return
			}
			handler.ServeHTTP(w, r)
		})}
	stop := context.AfterFunc(ctx, func() { server.Close() })
	defer stop()
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
		fmt.Fprintln(log, "客户机命令服务:", err)
	}
}
