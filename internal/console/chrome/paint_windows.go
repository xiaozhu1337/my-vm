//go:build windows

// Package chrome draws the console's native controls without changing their input behavior.
package chrome

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user     = windows.NewLazySystemDLL("user32.dll")
	gdi      = windows.NewLazySystemDLL("gdi32.dll")
	controls = windows.NewLazySystemDLL("comctl32.dll")
)

type Rect struct{ Left, Top, Right, Bottom int32 }

type Item struct {
	Kind, ID, Index, Action, State uint32
	Window, DC                     uintptr
	Bounds                         Rect
	Data                           uintptr
}

func ReadItem(address uintptr) (Item, bool) {
	var item Item
	err := windows.ReadProcessMemory(windows.CurrentProcess(), address, (*byte)(unsafe.Pointer(&item)), unsafe.Sizeof(item), nil)
	return item, err == nil && (item.Kind == 4 || item.Kind == 5) && item.DC != 0
}

type Painter struct {
	font         uintptr
	dpi          uintptr
	highContrast bool
	ownsFont     bool
}

func (p *Painter) Font(dpi uintptr) uintptr {
	if dpi == 0 {
		dpi = 96
	}
	if p.dpi == dpi && p.font != 0 {
		return p.font
	}
	font, _, _ := gdi.NewProc("CreateFontW").Call(uintptr(-int32(14*dpi/96)), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Segoe UI"))))
	if font == 0 {
		if p.font == 0 {
			p.font, _, _ = gdi.NewProc("GetStockObject").Call(17)
		}
	} else {
		if p.ownsFont {
			gdi.NewProc("DeleteObject").Call(p.font)
		}
		p.font, p.ownsFont = font, true
	}
	p.dpi = dpi
	p.SettingsChanged()
	return p.font
}

func (p *Painter) SettingsChanged() {
	var contrast struct {
		Size, Flags uint32
		Scheme      *uint16
	}
	contrast.Size = uint32(unsafe.Sizeof(contrast))
	user.NewProc("SystemParametersInfoW").Call(0x42, uintptr(contrast.Size), uintptr(unsafe.Pointer(&contrast)), 0)
	p.highContrast = contrast.Flags&1 != 0
}

func (p *Painter) Close() {
	if p.ownsFont {
		gdi.NewProc("DeleteObject").Call(p.font)
	}
	p.font, p.ownsFont = 0, false
}

func (p *Painter) Background(dc, window uintptr) {
	var rect Rect
	user.NewProc("GetClientRect").Call(window, uintptr(unsafe.Pointer(&rect)))
	fill(dc, rect, p.surface())
}

func (p *Painter) surface() uint32 {
	if p.highContrast {
		c, _, _ := user.NewProc("GetSysColor").Call(15)
		return uint32(color(uint32(c)))
	}
	return 0xf8fafc
}

func color(value uint32) uintptr { return uintptr(value>>16&255 | value&0xff00 | value&255<<16) }

func brush(dc uintptr, value uint32) uintptr {
	object, _, _ := gdi.NewProc("GetStockObject").Call(18)
	gdi.NewProc("SetDCBrushColor").Call(dc, color(value))
	return object
}

func fill(dc uintptr, rect Rect, value uint32) {
	object := brush(dc, value)
	user.NewProc("FillRect").Call(dc, uintptr(unsafe.Pointer(&rect)), object)
}

func rounded(dc uintptr, rect Rect, background, border uint32, radius int32) {
	gdi.NewProc("SelectObject").Call(dc, brush(dc, background))
	pen, _, _ := gdi.NewProc("GetStockObject").Call(19)
	gdi.NewProc("SelectObject").Call(dc, pen)
	gdi.NewProc("SetDCPenColor").Call(dc, color(border))
	gdi.NewProc("RoundRect").Call(dc, uintptr(rect.Left), uintptr(rect.Top), uintptr(rect.Right), uintptr(rect.Bottom), uintptr(radius), uintptr(radius))
}

type Button struct {
	Text                             string
	Selected, Primary, Danger, Close bool
}

type palette struct{ background, border, text uint32 }

func focusVisible(state uint32) bool { return state&0x10 != 0 && state&0x200 == 0 }

func buttonPalette(button Button, state uint32, hover bool) palette {
	result := palette{0xf8fafc, 0xf8fafc, 0x334155}
	if button.Selected {
		result = palette{0xe8efff, 0xc7d6fc, 0x2456c6}
	}
	if button.Primary {
		result = palette{0x3563d9, 0x3563d9, 0xffffff}
	}
	if button.Danger {
		result.text = 0xb42318
	}
	if hover && !button.Selected && !button.Primary {
		result.background, result.border = 0xffffff, 0xdce3ed
	}
	if state&1 != 0 {
		result.background, result.border = 0xdfe7f4, 0xbbc8dc
		result.text = 0x24477e
	}
	if state&4 != 0 && !button.Selected {
		result = palette{0xf8fafc, 0xf8fafc, 0x9aa5b5}
	}
	if focusVisible(state) {
		result.border = 0x163b85
	}
	return result
}

func (p *Painter) Button(item Item, button Button) {
	saved, _, _ := gdi.NewProc("SaveDC").Call(item.DC)
	defer gdi.NewProc("RestoreDC").Call(item.DC, saved)
	colors := buttonPalette(button, item.State, hovered[item.Window])
	if p.highContrast {
		bg, _, _ := user.NewProc("GetSysColor").Call(15)
		fg, _, _ := user.NewProc("GetSysColor").Call(18)
		if button.Selected || button.Primary {
			bg, _, _ = user.NewProc("GetSysColor").Call(13)
			fg, _, _ = user.NewProc("GetSysColor").Call(14)
		}
		if item.State&4 != 0 && !button.Selected {
			fg, _, _ = user.NewProc("GetSysColor").Call(17)
		}
		colors = palette{uint32(color(uint32(bg))), uint32(color(uint32(fg))), uint32(color(uint32(fg)))}
	}
	fill(item.DC, item.Bounds, p.surface())
	rect := item.Bounds
	rect.Left++
	rect.Top++
	rect.Right--
	rect.Bottom--
	rounded(item.DC, rect, colors.background, colors.border, int32(8*p.dpi/96))
	if p.highContrast && focusVisible(item.State) {
		focus := rect
		inset := int32(3 * p.dpi / 96)
		focus.Left += inset
		focus.Top += inset
		focus.Right -= inset
		focus.Bottom -= inset
		user.NewProc("DrawFocusRect").Call(item.DC, uintptr(unsafe.Pointer(&focus)))
	}
	if button.Close {
		x, y := (rect.Left+rect.Right)/2, (rect.Top+rect.Bottom)/2
		r := int32(4 * p.dpi / 96)
		gdi.NewProc("SetDCPenColor").Call(item.DC, color(colors.text))
		gdi.NewProc("MoveToEx").Call(item.DC, uintptr(x-r), uintptr(y-r), 0)
		gdi.NewProc("LineTo").Call(item.DC, uintptr(x+r+1), uintptr(y+r+1))
		gdi.NewProc("MoveToEx").Call(item.DC, uintptr(x+r), uintptr(y-r), 0)
		gdi.NewProc("LineTo").Call(item.DC, uintptr(x-r-1), uintptr(y+r+1))
		return
	}
	p.text(item.DC, rect, button.Text, colors.text, 0x825)
}

func (p *Painter) text(dc uintptr, rect Rect, text string, foreground uint32, flags uintptr) {
	gdi.NewProc("SelectObject").Call(dc, p.font)
	gdi.NewProc("SetBkMode").Call(dc, 1)
	gdi.NewProc("SetTextColor").Call(dc, color(foreground))
	user.NewProc("DrawTextW").Call(dc, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(text))), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), flags)
}

func (p *Painter) Banner(item Item, text string, failure bool) {
	saved, _, _ := gdi.NewProc("SaveDC").Call(item.DC)
	defer gdi.NewProc("RestoreDC").Call(item.DC, saved)
	fill(item.DC, item.Bounds, p.surface())
	rect := item.Bounds
	background, border, foreground := uint32(0xeff5ff), uint32(0xdde8fc), uint32(0x345381)
	if failure {
		background, border, foreground = 0xfff1f0, 0xf4d5d0, 0xa32720
	}
	if p.highContrast {
		bg, _, _ := user.NewProc("GetSysColor").Call(15)
		fg, _, _ := user.NewProc("GetSysColor").Call(18)
		background, border, foreground = uint32(color(uint32(bg))), uint32(color(uint32(fg))), uint32(color(uint32(fg)))
	}
	rounded(item.DC, rect, background, border, int32(8*p.dpi/96))
	rect.Left += int32(12 * p.dpi / 96)
	rect.Right -= int32(10 * p.dpi / 96)
	p.text(item.DC, rect, text, foreground, 0x8824)
}

// Track keeps standard BUTTON keyboard and accessibility behavior intact.
func Track(window uintptr) {
	hovered[window] = false
	controls.NewProc("SetWindowSubclass").Call(window, subclass, 1, 0)
}

var hovered = map[uintptr]bool{}
var subclass uintptr

func init() { subclass = windows.NewCallback(buttonProcedure) }

func buttonProcedure(hwnd uintptr, msg uint32, wp, lp, id, data uintptr) uintptr {
	switch msg {
	case 0x87:
		result, _, _ := controls.NewProc("DefSubclassProc").Call(hwnd, uintptr(msg), wp, lp)
		if wp == 13 {
			result |= 4
		}
		return result
	case 0x100:
		if wp == 13 {
			if lp&(1<<30) == 0 {
				user.NewProc("SendMessageW").Call(hwnd, 0xf5, 0, 0)
			}
			return 0
		}
	case 0x200:
		if !hovered[hwnd] {
			hovered[hwnd] = true
			var track struct {
				Size, Flags uint32
				Window      uintptr
				Hover       uint32
			}
			track.Size, track.Flags, track.Window = uint32(unsafe.Sizeof(track)), 2, hwnd
			user.NewProc("TrackMouseEvent").Call(uintptr(unsafe.Pointer(&track)))
			user.NewProc("InvalidateRect").Call(hwnd, 0, 0)
		}
	case 0x2a3:
		hovered[hwnd] = false
		user.NewProc("InvalidateRect").Call(hwnd, 0, 0)
	case 0x82:
		delete(hovered, hwnd)
		controls.NewProc("RemoveWindowSubclass").Call(hwnd, subclass, id)
	}
	result, _, _ := controls.NewProc("DefSubclassProc").Call(hwnd, uintptr(msg), wp, lp)
	return result
}
