//go:build windows

package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"my-vm/internal/vm"
)

type diskJob struct {
	machineID, targetID, name, label string
	copied, total                    int64
}

func (a *application) diskBusy(id string) bool {
	return a.diskJob != nil && (a.diskJob.machineID == id || a.diskJob.targetID == id)
}

func (a *application) flushDisk() {
	if !a.busy && a.diskDone != nil {
		finish := a.diskDone
		a.diskDone = nil
		finish()
	}
}

func (a *application) runDisk(label string, machine vm.Machine, targetID string, work func(func(int64, int64)) error, done func()) {
	if a.busy || a.loadError != nil || a.diskJob != nil {
		return
	}
	job := &diskJob{machineID: machine.ID, targetID: targetID, name: machine.Name, label: label}
	a.diskJob = job
	a.messageError = false
	a.message = label + "…"
	go func() {
		last := time.Time{}
		err := work(func(copied, total int64) {
			if time.Since(last) < 150*time.Millisecond && copied < total {
				return
			}
			last = time.Now()
			mygo.RunOnMain(func() {
				job.copied, job.total = copied, total
				if a.window != nil {
					a.window.Invalidate()
				}
			})
		})
		mygo.RunOnMain(func() {
			a.diskDone = func() {
				a.diskJob = nil
				if err != nil {
					a.report(fmt.Errorf("%s「%s」: %w", label, machine.Name, err))
				} else {
					a.messageError = false
					a.message = label + "「" + machine.Name + "」完成"
					if done != nil {
						done()
					}
				}
				if a.window != nil {
					a.window.Invalidate()
				}
			}
			a.flushDisk()
		})
	}()
}

func (j *diskJob) status() string {
	if j.total <= 0 {
		return j.label + "「" + j.name + "」…"
	}
	if j.copied >= j.total {
		return j.label + "「" + j.name + "」· 复制完成，正在保存…"
	}
	return fmt.Sprintf("%s「%s」· %.1f%% · %.2f / %.2f GiB", j.label, j.name, float64(j.copied)*100/float64(j.total), float64(j.copied)/(1<<30), float64(j.total)/(1<<30))
}
