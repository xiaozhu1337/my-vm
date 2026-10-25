package sharing

import (
	"io"
	"net/http"
	"time"
)

// idleHandler limits stalled transfers, without imposing a total file transfer time.
func idleHandler(next http.Handler, idle time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if r.Body != nil && r.Body != http.NoBody {
			// net/http may drain the original body without calling our Read wrapper.
			if err := controller.SetReadDeadline(time.Now().Add(idle)); err != nil {
				http.Error(w, "Cannot set request read deadline", http.StatusInternalServerError)
				return
			}
			r.Body = &idleBody{ReadCloser: r.Body, controller: controller, idle: idle}
		}
		next.ServeHTTP(&idleResponse{ResponseWriter: w, controller: controller, idle: idle}, r)
	})
}

type idleBody struct {
	io.ReadCloser
	controller *http.ResponseController
	idle       time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		_ = b.controller.SetReadDeadline(time.Time{})
	} else if n > 0 {
		_ = b.controller.SetReadDeadline(time.Now().Add(b.idle))
	}
	return n, err
}
func (b *idleBody) Close() error {
	defer b.controller.SetReadDeadline(time.Time{})
	return b.ReadCloser.Close()
}

type idleResponse struct {
	http.ResponseWriter
	controller *http.ResponseController
	idle       time.Duration
	started    bool
}

func (w *idleResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *idleResponse) begin() error {
	if w.started {
		return nil
	}
	w.started = true
	return w.controller.SetWriteDeadline(time.Now().Add(w.idle))
}
func (w *idleResponse) WriteHeader(status int) {
	_ = w.begin()
	w.ResponseWriter.WriteHeader(status)
}
func (w *idleResponse) Write(p []byte) (int, error) {
	if err := w.begin(); err != nil {
		return 0, err
	}
	total := 0
	for len(p) > 0 {
		part := p[:min(len(p), 32<<10)]
		n, err := w.ResponseWriter.Write(part)
		total += n
		p = p[n:]
		if n > 0 {
			_ = w.controller.SetWriteDeadline(time.Now().Add(w.idle))
		}
		if err != nil {
			return total, err
		}
		if n != len(part) {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
func (w *idleResponse) FlushError() error {
	if err := w.begin(); err != nil {
		return err
	}
	if err := w.controller.Flush(); err != nil {
		return err
	}
	return w.controller.SetWriteDeadline(time.Now().Add(w.idle))
}
func (w *idleResponse) Flush() { _ = w.FlushError() }
