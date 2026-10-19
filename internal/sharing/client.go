package sharing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"net/http/httputil"
	"time"
)

const Port uint32 = 19027
const ProxyAddress = "127.0.0.1:18389"

func RunProxy(ctx context.Context, log io.Writer, mapShares func([]Share) error) error {
	return RunProxyReady(ctx, log, mapShares, nil, nil)
}

// RunProxyReady distinguishes local startup from successful host folder mapping.
func RunProxyReady(ctx context.Context, log io.Writer, mapShares func([]Share) error, ready, mapped func(error)) error {
	listener, err := net.Listen("tcp", ProxyAddress)
	if ready != nil {
		ready(err)
	}
	if err != nil {
		return err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dialHost(ctx) }, MaxConnsPerHost: 8, MaxIdleConns: 8, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableCompression: true}
	defer transport.CloseIdleConnections()
	errorOutput := log
	if errorOutput == nil {
		errorOutput = io.Discard
	}
	errorLog := stdlog.New(errorOutput, "Shared folders: ", 0)
	proxy := &httputil.ReverseProxy{ErrorLog: errorLog, Transport: transport, Rewrite: func(r *httputil.ProxyRequest) {
		r.Out.URL.Scheme = "http"
		r.Out.URL.Host = "my-vm-host"
		r.Out.Host = r.In.Host
	}}
	server := &http.Server{ErrorLog: errorLog, Handler: proxy, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32768}
	defer server.Close()
	go func() { <-ctx.Done(); _ = server.Close() }()
	go func() {
		client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
		for ctx.Err() == nil {
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://my-vm-host/state", nil)
			response, err := client.Do(req)
			if err == nil {
				var shares []Share
				if response.StatusCode != http.StatusOK {
					err = fmt.Errorf("共享目录查询失败: HTTP %d", response.StatusCode)
				} else {
					err = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&shares)
				}
				response.Body.Close()
				if err == nil {
					err = mapShares(shares)
					if mapped != nil {
						mapped(err)
					}
					if err == nil {
						return
					}
				}
			}
			if mapped != nil {
				mapped(err)
			}
			if log != nil {
				fmt.Fprintln(log, "Shared folders:", err)
			}
			timer := time.NewTimer(3 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
