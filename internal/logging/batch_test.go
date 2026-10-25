package logging

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

type countingOutput struct {
	bytes.Buffer
	writes int
}

func (w *countingOutput) Write(p []byte) (int, error) { w.writes++; return w.Buffer.Write(p) }

func TestDrainBatchesRecordsAndFlushesOnClose(t *testing.T) {
	logger := &Logger{queue: make(chan []byte, 1000)}
	for range 1000 {
		logger.queue <- []byte("record\n")
	}
	close(logger.queue)
	output := &countingOutput{}
	logger.drain(output)
	if strings.Count(output.String(), "record\n") != 1000 {
		t.Fatal("log records lost")
	}
	if output.writes >= 10 {
		t.Fatal("log writes were not batched", output.writes)
	}
}

type recoveringOutput struct {
	bytes.Buffer
	failed chan struct{}
	writes int
}

func (w *recoveringOutput) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		close(w.failed)
		return 0, errors.New("disk full")
	}
	return w.Buffer.Write(p)
}

func TestDrainResumesAfterTemporaryWriteFailure(t *testing.T) {
	for _, size := range []int{32, (64 << 10) + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			logger := &Logger{queue: make(chan []byte, 2)}
			output := &recoveringOutput{failed: make(chan struct{})}
			done := make(chan struct{})
			go func() { defer close(done); logger.drain(output) }()
			logger.queue <- bytes.Repeat([]byte("x"), size)
			select {
			case <-output.failed:
			case <-time.After(2 * time.Second):
				t.Fatal("first log write did not occur")
			}
			logger.queue <- []byte("recovered\n")
			close(logger.queue)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("logger did not drain")
			}
			if output.String() != "recovered\n" {
				t.Fatal("logger did not recover after a write failure", output.String())
			}
		})
	}
}
