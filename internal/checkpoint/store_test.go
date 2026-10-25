package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeepCatalogValidationAndForkLimit(t *testing.T) {
	s := &Store{Version: 1, Active: "disk.vhdx", Layers: map[string]string{"disk.vhdx": ""}}
	parent, entryParent := s.Active, ""
	for i := range 10000 {
		disk := fmt.Sprintf("disk-%032x.vhdx", i+1)
		id := fmt.Sprintf("%032x", i+1)
		s.Layers[disk] = parent
		s.Entries = append(s.Entries, Entry{ID: id, Name: fmt.Sprintf("snapshot_%d", i), Disk: parent, Parent: entryParent})
		parent, entryParent = disk, id
	}
	s.Active, s.Current = parent, entryParent
	if err := s.Validate(); err != nil {
		t.Fatal("existing deep catalog must remain recoverable", err)
	}
	if len(s.Tree()) != len(s.Entries) {
		t.Fatal("deep tree lost entries")
	}
	if err := s.CheckFork(s.Active); err == nil {
		t.Fatal("hidden ancestry bypassed fork limit")
	}
	if err := s.CheckFork("disk.vhdx"); err != nil {
		t.Fatal("independent branch rejected", err)
	}
	if err := s.CheckFork(fmt.Sprintf("disk-%032x.vhdx", 62)); err != nil {
		t.Fatal("fork producing the 64th layer rejected", err)
	}
	if err := s.CheckFork(fmt.Sprintf("disk-%032x.vhdx", 63)); err == nil {
		t.Fatal("fork producing a 65th layer accepted")
	}
	s.Entries[0].Disk = s.Active
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "只读恢复点") {
		t.Fatal("writable recovery point accepted", err)
	}
	s.Entries[0].Disk = "disk.vhdx"
	s.Layers["disk.vhdx"] = s.Active
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "循环") {
		t.Fatal("disk cycle accepted", err)
	}
}

func TestBranchDeletionKeepsSharedAncestorsAndCollectsAbandonedLeaves(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(s.Path(name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(s.Active)
	fork := func(name string) Entry {
		t.Helper()
		e, err := NewEntry(name)
		if err != nil {
			t.Fatal(err)
		}
		e.Disk, e.Parent = s.Active, s.Current
		id, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		child := "disk-" + id + ".vhdx"
		s.Layers[child] = s.Active
		s.Pending = &Fork{Entry: e, Child: child}
		write(child)
		s.CommitFork()
		if err = s.Save(); err != nil {
			t.Fatal(err)
		}
		return e
	}
	a := fork("baseline")
	b := fork("branch_b")
	abandoned := s.Active
	id, _ := NewID()
	s.Active = "disk-" + id + ".vhdx"
	s.Current = a.ID
	s.Layers[s.Active] = a.Disk
	write(s.Active)
	c := fork("branch_c")
	s.Retire()
	if err = s.Save(); err != nil {
		t.Fatal(err)
	}
	if err = s.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.Path(abandoned)); !os.IsNotExist(err) {
		t.Fatal("abandoned writable leaf not collected", err)
	}
	if len(s.Tree()) != 3 || s.Tree()[1].ID != b.ID || s.Tree()[2].ID != c.ID {
		t.Fatal("branch traversal lost a sibling", s.Tree())
	}
	s.Remove(a.ID)
	s.Retire()
	if err = s.Save(); err != nil {
		t.Fatal(err)
	}
	if err = s.Collect(); err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{a, b, c} {
		if _, err = os.Stat(s.Path(e.Disk)); err != nil {
			t.Fatal("shared recovery disk removed", e, err)
		}
	}
	loaded, err := Load(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 2 || loaded.Entries[0].Parent != "" || loaded.Entries[1].Parent != "" || loaded.Current != c.ID {
		t.Fatal("delete broke snapshot tree", loaded)
	}
}

func TestManifestRejectsUnsafeOrIncompleteChains(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Store)
	}{
		{"escape", func(s *Store) { s.Active = "../outside.vhdx" }},
		{"missing active", func(s *Store) { delete(s.Layers, s.Active) }},
		{"cycle", func(s *Store) { s.Layers[s.Active] = s.Active }},
		{"missing parent", func(s *Store) { s.Layers[s.Active] = "disk-00000000000000000000000000000000.vhdx" }},
		{"collect active", func(s *Store) { s.Garbage = []string{s.Active} }},
		{"unknown current", func(s *Store) { s.Current = "absent" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			tc.change(s)
			if err = s.Save(); err == nil {
				t.Fatal("unsafe chain published")
			}
		})
	}
	dir := t.TempDir()
	for _, data := range []string{"version: 1\nunknown: true\n", "version: 1\n---\nversion: 1\n", "[invalid"} {
		if err := os.WriteFile(filepath.Join(dir, CatalogFile), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Fatal("corrupt manifest silently fell back to base disk")
		}
	}
}

func TestLegacySnapshotsAreImportedOnceAndRetainedAcrossSave(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "snapshots", "before_update")
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "disk.vhdx"), []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 1 || !s.Entries[0].Legacy {
		t.Fatal("legacy recovery point missing", s.Entries)
	}
	if err = s.Save(); err != nil {
		t.Fatal(err)
	}
	s, err = Load(dir)
	if err != nil || len(s.Entries) != 1 {
		t.Fatal("legacy import duplicated", err)
	}
	s.Remove(s.Entries[0].ID)
	s.Retire()
	if err = s.Save(); err != nil {
		t.Fatal(err)
	}
	s, err = Load(dir)
	if err != nil || len(s.Entries) != 0 {
		t.Fatal("retired legacy point resurrected before collection", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, CatalogFile))
	if err != nil || !strings.Contains(string(data), "# Storage manifest") {
		t.Fatal("configuration comments missing", err)
	}
	if err = s.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("empty legacy directory not removed", err)
	}
}
