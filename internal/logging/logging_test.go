package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentLoggingFlush(t *testing.T) {
	dir := t.TempDir()
	logger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 20; j++ {
				_, _ = fmt.Fprintln(logger, "test message")
			}
		}()
	}
	workers.Wait()
	logger.Close()
	data, err := os.ReadFile(filepath.Join(dir, "my-vm.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "test message") != 400 {
		t.Fatal("log records lost")
	}
	if _, err = logger.Write([]byte("closed")); err == nil {
		t.Fatal("closed logger accepted write")
	}
}
