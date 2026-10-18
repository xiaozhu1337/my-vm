//go:build windows

package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/theme"
)

func appTheme(c *ui.Context) *ui.Theme {
	t := theme.Onevra(c)
	if t.Dark {
		t.Background, t.Surface = ui.Hex("#111821"), ui.Hex("#192330")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#253346"), ui.Hex("#30425a")
		t.Text, t.TextMuted, t.Border = ui.Hex("#e9eff8"), ui.Hex("#a0afc4"), ui.Hex("#344358")
		t.Accent, t.AccentText = ui.Hex("#85adff"), ui.Hex("#102345")
	} else {
		t.Background, t.Surface = ui.Hex("#f4f6fa"), ui.Hex("#ffffff")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#edf2fa"), ui.Hex("#dfe8f7")
		t.Text, t.TextMuted, t.Border = ui.Hex("#202d43"), ui.Hex("#64748b"), ui.Hex("#e1e7f0")
		t.Accent, t.AccentText = ui.Hex("#3266dc"), ui.Hex("#ffffff")
	}
	t.AccentHover = t.Accent.Mix(t.Text, .12)
	t.AccentPressed = t.Accent.Mix(t.Text, .24)
	t.Selection, t.Focus = t.Accent.Alpha(.22), t.Accent.Alpha(.65)
	t.Radius = 9
	if c.Preferences().HighContrast {
		t.Border = t.Border.Mix(t.Text, .45)
		t.TextMuted = t.TextMuted.Mix(t.Text, .4)
		t.Focus.A = 255
	}
	return t
}

func text(c *ui.Context, s string) { components.Text(c, s) }
func muted(c *ui.Context, s string) {
	components.Text(c, s).FontSize(c.Theme().Rem(.9)).TextColor(c.Theme().TextMuted)
}

func action(c *ui.Context, label string, enabled bool, variant components.ButtonVariant) bool {
	clicked := false
	ui.Row(c).Key(label).Disabled(!enabled).Children(func() {
		clicked = components.Button(c, label, variant).MinHeight(38).Clicked()
	})
	return enabled && clicked
}

func button(c *ui.Context, label string, enabled bool) bool {
	return action(c, label, enabled, components.ButtonOutline)
}

func card(c *ui.Context, title, description string, content func()) {
	ui.Column(c).Key(title).FillWidth().MinWidth(0).Gap(16).Padding(22).
		Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(12).Children(func() {
		ui.Column(c).FillWidth().Gap(5).Children(func() {
			components.Text(c, title).FontSize(17).FontWeight(600)
			if description != "" {
				muted(c, description)
			}
		})
		content()
	})
}

func pageHeading(c *ui.Context, title, description string) {
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		components.Text(c, title).FontSize(28).FontWeight(600).SingleLine()
		muted(c, description)
	})
}

func scrollBody(c *ui.Context, key string, content func()) {
	ui.Column(c).Key(key).Fill().MinHeight(0).MinWidth(0).Children(func() {
		components.ScrollView(c, components.ScrollViewOptions{}).Fill().Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).Gap(18).Padding(0, 4, 8, 0).Children(content)
		})
	})
}

func stateBadge(c *ui.Context, state string) {
	tone := components.BadgeNeutral
	if state == "运行中" {
		tone = components.BadgeSuccess
	} else if state == "已暂停" {
		tone = components.BadgeWarning
	}
	components.Badge(c, state, components.BadgeOptions{Tone: tone})
}
