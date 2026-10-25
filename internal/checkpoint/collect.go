package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
)

func (s *Store) liveFiles() map[string]bool {
	live := map[string]bool{}
	var disk func(string)
	disk = func(name string) {
		if name == "" || live[name] {
			return
		}
		live[name] = true
		disk(s.Layers[name])
	}
	disk(s.Active)
	for _, e := range s.Entries {
		disk(e.Disk)
		for _, file := range e.StateFiles() {
			live[file] = true
		}
	}
	if s.Restoring != nil {
		disk(s.Restoring.Active)
		if s.Restoring.Backup != "" {
			live[s.Restoring.Backup] = true
		}
		if s.Restoring.Rollback != nil {
			for _, file := range s.Restoring.Rollback.StateFiles() {
				live[file] = true
			}
		}
	}
	if s.Pending != nil {
		disk(s.Pending.Child)
		for _, file := range s.Pending.Entry.StateFiles() {
			live[file] = true
		}
	}
	return live
}

// Retire removes recovery points, never disks still needed by another branch.
func (s *Store) Retire(files ...string) {
	live := s.liveFiles()
	for name := range s.Layers {
		if !live[name] {
			files = append(files, name)
			delete(s.Layers, name)
		}
	}
	for _, name := range files {
		if name != "" && !live[name] && !slices.Contains(s.Garbage, name) {
			s.Garbage = append(s.Garbage, name)
		}
	}
}

// Collect is retryable. A failed removal remains recorded for a later operation.
func (s *Store) Collect() error {
	if err := s.Validate(); err != nil {
		return err
	}
	live := s.liveFiles()
	var pending []string
	var errs []error
	for _, name := range s.Garbage {
		if live[name] {
			return errors.New("待清理文件仍被虚拟机或快照引用")
		}
		path := s.Path(name)
		if _, err := os.Lstat(path); err == nil {
			if err = s.CheckFiles(name); err != nil {
				errs = append(errs, err)
				pending = append(pending, name)
				continue
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			pending = append(pending, name)
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			pending = append(pending, name)
			continue
		}
		folder := filepath.Dir(path)
		if filepath.Dir(folder) == s.Path("snapshots") {
			_ = os.Remove(folder)
		}
	}
	s.Garbage = pending
	if err := s.Save(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
