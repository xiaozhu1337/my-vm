//go:build windows

package chrome

import (
	"testing"
	"unsafe"
)

func TestButtonStatesRemainDistinct(t *testing.T) {
	normal := buttonPalette(Button{}, 0, false)
	selected := buttonPalette(Button{Selected: true}, 4, false)
	disabled := buttonPalette(Button{}, 4, false)
	if selected == disabled || selected.text == disabled.text || selected.background == normal.background {
		t.Fatal("the current display mode looks like a disabled action")
	}
	if buttonPalette(Button{}, 0, true) == normal || buttonPalette(Button{}, 1, false) == normal {
		t.Fatal("hover and pressed feedback are missing")
	}
	if buttonPalette(Button{Danger: true}, 0, false).text == normal.text {
		t.Fatal("shutdown lost its distinct appearance")
	}
	for _, button := range []Button{{}, {Primary: true}, {Selected: true}, {Close: true}} {
		plain := buttonPalette(button, 0, false)
		focused := buttonPalette(button, 0x10, false)
		if focused.border == plain.border || focused.border == focused.background {
			t.Fatal("keyboard focus is invisible", button, focused)
		}
		if buttonPalette(button, 0x210, false) != plain {
			t.Fatal("system focus-cue suppression was ignored")
		}
	}
}

func TestHighContrastFocusDecision(t *testing.T) {
	for _, state := range []uint32{0, 4, 0x200, 0x210} {
		if focusVisible(state) {
			t.Fatal("unexpected focus cue", state)
		}
	}
	if !focusVisible(0x10) {
		t.Fatal("keyboard focus must also be drawn after applying high-contrast colors")
	}
}

func TestDrawItemLayoutAndColors(t *testing.T) {
	var item Item
	if unsafe.Sizeof(item) != 64 || unsafe.Offsetof(item.Window) != 24 || unsafe.Offsetof(item.DC) != 32 || unsafe.Offsetof(item.Bounds) != 40 {
		t.Fatal("DRAWITEMSTRUCT does not match the Windows x64 ABI")
	}
	if color(0x123456) != 0x563412 || color(uint32(color(0x123456))) != 0x123456 {
		t.Fatal("RGB and COLORREF were mixed")
	}
}
