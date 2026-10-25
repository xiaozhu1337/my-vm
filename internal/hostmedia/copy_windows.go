//go:build windows

package hostmedia

import (
	"sync"
	"sync/atomic"
)

type copyTask struct{ source, target string }

// copyParallel joins both workers before the caller may clean up staging files.
func copyParallel(tasks []copyTask, copyFile func(string, string) (fileRecord, error)) ([]fileRecord, error) {
	records := make([]fileRecord, len(tasks))
	var next atomic.Int64
	var failed atomic.Bool
	var once sync.Once
	var first error
	var workers sync.WaitGroup
	for range min(2, len(tasks)) {
		workers.Go(func() {
			for !failed.Load() {
				i := int(next.Add(1) - 1)
				if i >= len(tasks) {
					return
				}
				record, err := copyFile(tasks[i].source, tasks[i].target)
				if err != nil {
					once.Do(func() { first = err; failed.Store(true) })
					return
				}
				records[i] = record
			}
		})
	}
	workers.Wait()
	return records, first
}
