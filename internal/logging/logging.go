package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Logger struct {
	queue  chan []byte
	done   chan struct{}
	mu     sync.RWMutex
	closed bool
}

func Open(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "my-vm.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	l := &Logger{queue: make(chan []byte, 256), done: make(chan struct{})}
	go func() {
		defer close(l.done)
		defer f.Close()
		for p := range l.queue {
			if _, err := f.Write(p); err != nil {
				fmt.Fprintln(os.Stderr, "日志写入失败:", err)
			}
		}
		if err := f.Sync(); err != nil {
			fmt.Fprintln(os.Stderr, "日志同步失败:", err)
		}
	}()
	return l, nil
}

func (l *Logger) Write(p []byte) (int, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return 0, os.ErrClosed
	}
	entry := []byte(time.Now().Format(time.RFC3339) + " " + string(p))
	l.queue <- entry
	return len(p), nil
}
func (l *Logger) Close() {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.queue)
	}
	l.mu.Unlock()
	<-l.done
}
