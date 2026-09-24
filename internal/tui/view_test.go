package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazyfind/internal/domain"
)

func TestLayoutClampsAndPreservesUnicode(t *testing.T) {
	m := historyModel(t)
	m.cfg.UI.Color = "never"
	name := "專案 é 👩🏽‍💻.txt"
	m.items = map[string]domain.Item{"unicode": {ID: "unicode", Name: name, Path: "/" + name, Kind: "file"}}
	m.selection = "unicode"
	m.rebuild()
	for _, size := range [][2]int{{120, 32}, {80, 24}, {40, 12}, {10, 5}, {1, 1}, {0, 0}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			v := m.View()
			lines := strings.Split(v.Content, "\n")
			if len(lines) != max(1, size[1]) {
				t.Fatalf("height %d", len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > max(1, size[0]) {
					t.Fatalf("overflow %q", line)
				}
			}
		})
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	if !strings.Contains(m.View().Content, name) {
		t.Fatal("lost Unicode name at usable width")
	}
}
func TestMouseReleaseMustStillHitSameItem(t *testing.T) {
	m := historyModel(t)
	m.cfg.History.Enabled = false
	m.mouse = true
	m.rows = []domain.Item{{ID: "one", Name: "one"}, {ID: "two", Name: "two"}}
	m.selection = "one"
	m.selected = 0
	m.mouseClick(tea.Mouse{X: 2, Y: 7, Button: tea.MouseLeft})
	m.rows[0], m.rows[1] = m.rows[1], m.rows[0]
	if cmd := m.mouseRelease(tea.Mouse{X: 2, Y: 7, Button: tea.MouseLeft}); cmd != nil {
		t.Fatal("reordered row activated by stale press")
	}
	m.mouseClick(tea.Mouse{X: 2, Y: 7, Button: tea.MouseLeft})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd := m.mouseRelease(tea.Mouse{X: 2, Y: 7, Button: tea.MouseLeft}); cmd != nil {
		t.Fatal("resize retained mouse authority")
	}
}
func TestModalConsumesMouseWithoutBackgroundMutation(t *testing.T) {
	m := historyModel(t)
	m.overlay = &overlay{kind: "help", title: "Help"}
	before := append([]string(nil), m.query.Sources...)
	m.mouseClick(tea.Mouse{X: 12, Y: 3, Button: tea.MouseLeft})
	m.mouseRelease(tea.Mouse{X: 12, Y: 3, Button: tea.MouseLeft})
	if fmt.Sprint(before) != fmt.Sprint(m.query.Sources) {
		t.Fatal("modal click toggled source behind it")
	}
}
func TestHeaderMouseAndKeyboardSortPreserveSelectedID(t *testing.T) {
	m := historyModel(t)
	one, two := int64(1), int64(20)
	m.items = map[string]domain.Item{"one": {ID: "one", Name: "alpha", Size: &two}, "two": {ID: "two", Name: "beta", Size: &one}}
	m.selection = "one"
	m.rebuild()
	g := m.layout()
	x := g.nameWidth + 8
	m.mouseClick(tea.Mouse{X: x, Y: 5, Button: tea.MouseLeft})
	m.mouseRelease(tea.Mouse{X: x, Y: 5, Button: tea.MouseLeft})
	if m.sort != "size" || m.current().ID != "one" || m.rows[0].ID != "two" {
		t.Fatal("sorting changed selected identity")
	}
}
