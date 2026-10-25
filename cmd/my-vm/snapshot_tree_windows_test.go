//go:build windows

package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	vm "my-vm/library"
)

func TestFixedSnapshotLayoutKeepsBranchesAndSize(t *testing.T) {
	data := snapshotDiagram([]vm.Snapshot{
		{ID: "a", Name: "base"},
		{ID: "b", Name: "first", ParentID: "a"},
		{ID: "c", Name: "second", ParentID: "a", Current: true},
	})
	positions, width, height := snapshotLayout(data)
	if len(positions) != 5 || positions["b"].x != positions["c"].x || positions["b"].y >= positions["c"].y || positions[snapshotCurrent].x <= positions["c"].x {
		t.Fatal("lost fixed tree branches", positions)
	}
	for id, position := range positions {
		if position.x < 0 || position.y < 0 || position.x+snapshotNodeWidth > width || position.y+snapshotNodeHeight > height {
			t.Fatal("node outside scrollable content", id, position, width, height)
		}
	}
	a := &application{snapshotSelected: "a"}
	tt := ui.NewTester(func(c *ui.Context) { a.fixedSnapshotTree(c, data) }, 800, 340)
	before, ok := tt.Find("first")
	if !ok {
		t.Fatal("snapshot not accessible")
	}
	tt.Scroll(250, 160, 200, 100)
	after, ok := tt.Find("first")
	if !ok || before.W != after.W || before.H != after.H {
		t.Fatal("scroll changed node size", before, after)
	}
}
