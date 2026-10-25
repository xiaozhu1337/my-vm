//go:build windows

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	vm "my-vm/library"
)

type diskJob struct {
	machineID, targetID, name, label string
	copied, total                    int64
}

func (a *application) syncJobs() {
	external := map[string]bool{}
	for _, machine := range a.config.Machines {
		progress := a.manager.Progress(machine.ID)
		if progress == nil {
			continue
		}
		if local := a.diskJobs[machine.ID]; local != nil {
			local.copied, local.total = progress.Completed, progress.Total
		} else {
			external[machine.ID] = true
		}
		if progress.Target != "" {
			external[progress.Target] = true
		}
	}
	for id := range a.externalJobs {
		if !external[id] && id == a.selected {
			a.resetSnapshots()
		}
	}
	a.externalJobs = external
}

func (a *application) diskBusy(id string) bool {
	if a.operations[id] != "" || a.externalJobs[id] {
		return true
	}
	for _, job := range a.diskJobs {
		if job.machineID == id || job.targetID == id {
			return true
		}
	}
	return false
}

func (a *application) activeDiskJob() *diskJob {
	var selected *diskJob
	for _, job := range a.diskJobs {
		if job.machineID == a.selected || job.targetID == a.selected {
			return job
		}
		if selected == nil || job.machineID < selected.machineID {
			selected = job
		}
	}
	return selected
}

func (a *application) runDisk(label string, machine vm.Machine, targetID string, work func(func(int64, int64)) error, done func()) {
	if a.busy || a.loadError != nil || a.diskBusy(machine.ID) || targetID != "" && a.diskBusy(targetID) {
		return
	}
	job := &diskJob{machineID: machine.ID, targetID: targetID, name: machine.Name, label: label}
	if a.diskJobs == nil {
		a.diskJobs = map[string]*diskJob{}
	}
	a.diskJobs[machine.ID] = job
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
		cfg, refreshError := a.refresh()
		if err == nil && refreshError != nil {
			err = fmt.Errorf("操作已完成，但刷新失败，请核对: %w", refreshError)
		}
		mygo.RunOnMain(func() {
			if refreshError == nil {
				a.config = cfg
			}
			delete(a.diskJobs, machine.ID)
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
		})
	}()
}

func (j *diskJob) status() string {
	if j.total <= 0 {
		return j.label + "「" + j.name + "」…"
	}
	if strings.Contains(j.label, "快照") {
		if j.copied >= j.total {
			return j.label + "「" + j.name + "」· 100% · 正在保存…"
		}
		return fmt.Sprintf("%s「%s」· %.1f%%", j.label, j.name, float64(j.copied)*100/float64(j.total))
	}
	if j.copied >= j.total {
		return j.label + "「" + j.name + "」· 复制完成，正在保存…"
	}
	return fmt.Sprintf("%s「%s」· %.1f%% · %.2f / %.2f GiB", j.label, j.name, float64(j.copied)*100/float64(j.total), float64(j.copied)/(1<<30), float64(j.total)/(1<<30))
}
