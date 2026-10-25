//go:build windows

package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components/flow"
)

const snapshotNodeWidth float32 = 200
const snapshotNodeHeight float32 = 80

type snapshotPosition struct{ x, y float32 }

// Parents precede children in the library's tree order; the working leaf is last.
func snapshotLayout(data flow.Data) (map[string]snapshotPosition, float32, float32) {
	parents, children := map[string]string{}, map[string][]string{}
	for _, edge := range data.Edges {
		parents[edge.Target] = edge.Source
		children[edge.Source] = append(children[edge.Source], edge.Target)
	}
	positions := make(map[string]snapshotPosition, len(data.Nodes))
	width, row := float32(0), float32(0)
	for _, node := range data.Nodes {
		x := float32(16)
		if parent := parents[node.ID]; parent != "" {
			x = positions[parent].x + snapshotNodeWidth + 48
		}
		positions[node.ID] = snapshotPosition{x: x}
		width = max(width, x+snapshotNodeWidth+16)
	}
	for i := len(data.Nodes) - 1; i >= 0; i-- {
		id := data.Nodes[i].ID
		position := positions[id]
		if list := children[id]; len(list) > 0 {
			position.y = (positions[list[0]].y + positions[list[len(list)-1]].y) / 2
		} else {
			position.y = row
			row += snapshotNodeHeight + 24
		}
		positions[id] = position
	}
	height := max(snapshotNodeHeight+32, row+8)
	for id, position := range positions {
		position.y = height - position.y - snapshotNodeHeight - 16
		positions[id] = position
	}
	return positions, width, height
}

func (a *application) fixedSnapshotTree(c *ui.Context, data flow.Data) {
	positions, width, height := snapshotLayout(data)
	ui.ScrollBoth(c).Key("snapshot-tree").Label("虚拟机快照树").FillWidth().Height(320).
		TrackScroll(&a.snapshotTree).Background(c.Theme().Background).Border(1, c.Theme().Border).Radius(8).Children(func() {
		ui.Box(c).Size(width, height).Draw(func(p *ui.Painter, r ui.Rect) {
			for _, edge := range data.Edges {
				from, to := positions[edge.Source], positions[edge.Target]
				x1, y1 := r.X+from.x+snapshotNodeWidth, r.Y+from.y+snapshotNodeHeight/2
				x2, y2 := r.X+to.x, r.Y+to.y+snapshotNodeHeight/2
				middle := (x1 + x2) / 2
				var path ui.Path
				path.MoveTo(x1, y1).LineTo(middle, y1).LineTo(middle, y2)
				if edge.Dashed {
					for x := middle; x < x2; x += 8 {
						path.MoveTo(x, y2).LineTo(min(x+4, x2), y2)
					}
				} else {
					path.LineTo(x2, y2)
				}
				p.StrokePath(&path, 1.5, c.Theme().Border)
			}
		}).Children(func() {
			for _, node := range data.Nodes {
				position := positions[node.ID]
				box := ui.Box(c).Key(node.ID).Absolute().Left(position.x).Top(position.y).
					Size(snapshotNodeWidth, snapshotNodeHeight).Focusable().Role(ui.RoleButton).Label(node.Label).Cursor(ui.CursorPointer)
				if node.ID == a.snapshotSelected {
					box.Border(2, c.Theme().Accent).Radius(8)
				}
				if box.Clicked() {
					a.snapshotSelected = node.ID
				}
				box.Children(func() { node.Content(c) })
			}
		})
	})
}
