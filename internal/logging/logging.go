package logging

import (
	"bufio"
	"fmt"
	"io"
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
	l := &Logger{queue: make(chan []byte, 4096), done: make(chan struct{})}
	go func() {
		defer close(l.done)
		defer f.Close()
		l.drain(f)
		if err := f.Sync(); err != nil {
			fmt.Fprintln(os.Stderr, "日志同步失败:", err)
		}
	}()
	return l, nil
}

func (l *Logger) drain(output io.Writer) {
	buffer := bufio.NewWriterSize(output, 64<<10)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	flush := func() {
		if err := buffer.Flush(); err != nil {
			fmt.Fprintln(os.Stderr, "日志写入失败，丢弃未写出的缓冲记录:", err)
			buffer.Reset(output)
		}
	}
	defer flush()
	for {
		select {
		case entry, ok := <-l.queue:
			if !ok {
				return
			}
			if _, err := buffer.Write(entry); err != nil {
				fmt.Fprintln(os.Stderr, "日志写入失败，丢弃未写出的缓冲记录:", err)
				buffer.Reset(output)
			}
		case <-ticker.C:
			flush()
		}
	}
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
