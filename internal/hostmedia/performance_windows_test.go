//go:build windows

package hostmedia

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestArchiveOutputRejectsExcessBeforeBuffering(t *testing.T) {
	for _, test := range []struct {
		name         string
		limit, lines int
		input        string
	}{{"bytes", 4, 0, "12345"}, {"lines", 100, 2, "a\nb\nc\n"}} {
		t.Run(test.name, func(t *testing.T) {
			cancelled := false
			output := &archiveOutput{limit: test.limit, maxLines: test.lines, cancel: func() { cancelled = true }}
			if _, err := output.Write([]byte(test.input)); err == nil || !cancelled || output.Len() != 0 {
				t.Fatal("oversized output was retained", output.Len(), err)
			}
		})
	}
	output := &archiveOutput{limit: 16, maxLines: 2, cancel: func() { t.Fatal("valid output cancelled") }}
	for _, part := range []string{"a\n", "b\n"} {
		if _, err := output.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if output.String() != "a\nb\n" {
		t.Fatal(output.String())
	}
}

func TestCopyParallelBoundsWorkersAndJoinsFailure(t *testing.T) {
	var active, maxActive, completed atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	result := make(chan error, 1)
	tasks := []copyTask{{source: "fail"}, {source: "ok"}, {source: "unused"}}
	go func() {
		_, err := copyParallel(tasks, func(source, target string) (fileRecord, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for previous := maxActive.Load(); n > previous && !maxActive.CompareAndSwap(previous, n); previous = maxActive.Load() {
			}
			started <- struct{}{}
			<-release
			completed.Add(1)
			if source == "fail" {
				return fileRecord{}, errors.New("copy failed")
			}
			return fileRecord{Name: source}, nil
		})
		result <- err
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("copy worker did not start")
		}
	}
	close(release)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "copy failed") {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("copy did not join workers")
	}
	if active.Load() != 0 || maxActive.Load() != 2 || completed.Load() < 2 {
		t.Fatal("workers escaped cleanup", active.Load(), maxActive.Load(), completed.Load())
	}
}
