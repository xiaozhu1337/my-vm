package checkpoint

import (
	"os"
	"testing"
)

func TestStateFilesFollowRecoveryPointLifetime(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := NewEntry("full")
	entry.Disk, entry.State = s.Active, "running"
	id, _ := NewID()
	child := "disk-" + id + ".vhdx"
	s.Layers[child] = s.Active
	s.Pending = &Fork{Entry: entry, Child: child}
	for _, file := range append(entry.StateFiles(), s.Active, child) {
		if err = os.WriteFile(s.Path(file), []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.Retire(entry.StateFiles()...)
	if len(s.Garbage) != 0 {
		t.Fatal("pending state became garbage")
	}
	s.CommitFork()
	s.Retire(entry.StateFiles()...)
	if len(s.Garbage) != 0 {
		t.Fatal("committed state became garbage")
	}
	if err = s.Save(); err != nil {
		t.Fatal(err)
	}
	s, err = Load(s.Dir)
	if err != nil || s.Entries[0].State != "running" {
		t.Fatal("state metadata did not round-trip", err)
	}
	s.Remove(entry.ID)
	s.Retire(entry.StateFiles()...)
	if err = s.Collect(); err != nil {
		t.Fatal(err)
	}
	for _, file := range entry.StateFiles() {
		if _, err = os.Stat(s.Path(file)); !os.IsNotExist(err) {
			t.Fatal("deleted snapshot state remains", file, err)
		}
	}
	if _, err = os.Stat(s.Path(s.Active)); err != nil {
		t.Fatal("active disk was removed", err)
	}
	for _, file := range []string{"../state-a.vmrs", "state-xyz.vmgs", "state-00000000000000000000000000000000.exe"} {
		s.Garbage = []string{file}
		if s.Validate() == nil {
			t.Fatal("unsafe state deletion accepted", file)
		}
	}
}
