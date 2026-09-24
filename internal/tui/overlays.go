package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/history"
	"github.com/daviddwlee84/lazyfind/internal/hosts"
)

type actionsMsg struct {
	gen      int
	dialog   int
	id       string
	resolved actions.Resolved
	err      error
}
type rootsMsg struct {
	gen      int
	choices  []choice
	problems []string
}

func field(prompt, value string) textinput.Model {
	f := textinput.New()
	f.Prompt = prompt + ": "
	f.SetValue(value)
	f.CharLimit = 4096
	return f
}
func (m *Model) showActions() tea.Cmd {
	i := m.current()
	if i == nil {
		return nil
	}
	m.actionsGen++
	m.overlay = &overlay{kind: "actions", title: "Actions — Enter executes · Esc closes", input: field("Find action", ""), choices: m.commonActions()}
	m.overlay.input.Focus()
	if m.historical {
		m.overlay.choices = append(m.overlay.choices, choice{label: "Load live actions (recheck target)…", value: "load-actions"})
		return nil
	}
	return tea.Batch(m.accept(), m.resolveActions())
}
func (m *Model) commonActions() []choice {
	return []choice{{label: "Copy path / location / reference…", value: "copy-menu"}, {label: "Directory disk usage (recursive, may be slow)…", value: "directory-usage"}}
}
func (m *Model) resolveActions() tea.Cmd {
	m.syncMatchSelection()
	if m.current() == nil {
		return nil
	}
	item := *m.actionItem()
	svc := m.actionSvc
	q := m.query.Text
	gen := m.gen
	dialog := m.actionsGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
		defer cancel()
		r, e := svc.Resolve(ctx, item, q)
		return actionsMsg{gen: gen, dialog: dialog, id: item.ID, resolved: r, err: e}
	}
}
func (m *Model) showSources() {
	o := &overlay{kind: "sources", title: "Sources — Space toggles · Enter applies"}
	for _, s := range []string{"names", "text", "documents", "recent"} {
		o.choices = append(o.choices, choice{label: map[string]string{"names": "Names · fd", "text": "Text content · rg", "documents": "Documents · rga (optional)", "recent": "Frequent directories · zoxide (optional)"}[s], value: s, checked: contains(m.query.Sources, s)})
	}
	m.overlay = o
}
func (m *Model) showFilters() tea.Cmd {
	f := m.query.Filters
	if current, err := domain.ParseQuery(m.input.Value(), m.query); err == nil {
		f = current.Filters
	}
	minS, maxS := "", ""
	if f.MinSize != nil {
		minS = strconv.FormatInt(*f.MinSize, 10) + "B"
	}
	if f.MaxSize != nil {
		maxS = strconv.FormatInt(*f.MaxSize, 10) + "B"
	}
	o := &overlay{kind: "filters", title: "Filters — Tab field · Enter apply · Esc cancel"}
	labels := []string{"Type (file,dir,symlink)", "Extensions (comma-separated)", "Modified within (7d)", "After (inclusive date)", "Before (exclusive date)", "Minimum size", "Maximum size", "Maximum depth (0=unlimited)", "Include hidden files/folders", "Include ignored files/folders", "Use regex", "Match full path"}
	values := []string{strings.Join(f.Kinds, ","), strings.Join(f.Extensions, ","), f.ModifiedWithin, f.After, f.Before, minS, maxS, strconv.Itoa(f.Depth), strconv.FormatBool(f.Hidden), strconv.FormatBool(f.Ignored), strconv.FormatBool(m.query.Regex), strconv.FormatBool(m.query.FullPath)}
	for i, l := range labels {
		o.fields = append(o.fields, field(l, values[i]))
	}
	m.overlay = o
	return o.fields[0].Focus()
}
func (m *Model) showRoots() tea.Cmd {
	m.rootsGen++
	gen := m.rootsGen
	o := &overlay{kind: "roots", title: "Scope — Tab fields/list · Enter apply · Ctrl+N add root · Esc cancel", fields: []textinput.Model{field("SSH alias (empty=local)", m.query.Target.Host)}, field: 1}
	for _, p := range m.query.Roots {
		o.fields = append(o.fields, field("Root", p))
	}
	if len(o.fields) == 1 {
		o.fields = append(o.fields, field("Root", "."))
	}
	if m.query.Target.Remote() {
		o.choices = []choice{{label: "Open native SSH session · " + m.query.Target.Host, value: "ssh-session:" + m.query.Target.Host}}
	}
	m.overlay = o
	cfg := m.cfg
	target := m.query.Target
	return tea.Batch(o.fields[1].Focus(), func() tea.Msg {
		var cs []choice
		if target.Remote() {
			cs = append(cs, choice{label: "Open native SSH session · " + target.Host, value: "ssh-session:" + target.Host})
		}
		add := func(label, host string, roots []string) {
			b, _ := json.Marshal(domain.QuerySpec{Target: domain.Target{Host: host}, Roots: roots})
			cs = append(cs, choice{label: label, value: string(b)})
		}
		if cwd, e := os.Getwd(); e == nil {
			add("Local · current directory", "", []string{cwd})
			cmd := exec.CommandContext(m.ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
			if b, e := cmd.Output(); e == nil {
				add("Local · Git root", "", []string{strings.TrimSpace(string(b))})
			}
		}
		if home, e := os.UserHomeDir(); e == nil {
			add("Local · home", "", []string{home})
		}
		for _, r := range cfg.Roots {
			add(r.Name+" · "+strings.Join(r.Paths, ", "), r.Host, r.Paths)
		}
		hs, problems := hosts.Discover(m.ctx, cfg)
		for _, h := range hs {
			add("SSH · "+h.Name+" ["+h.Source+"]", h.Alias, []string{"~"})
		}
		if !target.Remote() {
			ctx, cancel := context.WithTimeout(m.ctx, 2*time.Second)
			defer cancel()
			if rs, e := hosts.RecentRoots(ctx, cfg, target); e == nil {
				for _, p := range rs {
					add("zoxide · "+p, "", []string{p})
				}
			}
		}
		if rs, e := history.New(cfg).List(m.ctx, 30); e == nil {
			seen := map[string]bool{}
			for _, r := range rs {
				key := r.Query.Target.ID() + strings.Join(r.Query.Roots, "\x00")
				if !seen[key] {
					seen[key] = true
					add("Recent · "+r.Query.Target.ID()+" · "+strings.Join(r.Query.Roots, ", "), r.Query.Target.Host, r.Query.Roots)
				}
			}
		}
		return rootsMsg{gen: gen, choices: cs, problems: problems}
	})
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
func (o *overlay) visible() []choice {
	if o.input.Value() == "" {
		return o.choices
	}
	var cs []choice
	for _, c := range o.choices {
		if domain.Fuzzy(c.label, o.input.Value()) {
			cs = append(cs, c)
		}
	}
	return cs
}
func (m *Model) overlayKey(k tea.KeyPressMsg) tea.Cmd {
	o := m.overlay
	key := k.String()
	if key == "esc" {
		m.overlay = o.parent
		m.pressed = ""
		return nil
	}
	if o.kind == "delete-history" {
		if key == "enter" {
			return m.activateChoice()
		}
	}
	if len(o.fields) > 0 && o.field >= 0 {
		if o.kind == "filters" && o.field >= 8 && key == " " {
			o.fields[o.field].SetValue(strconv.FormatBool(o.fields[o.field].Value() != "true"))
			return nil
		}
		switch key {
		case "tab", "shift+tab":
			o.fields[o.field].Blur()
			delta := 1
			if key == "shift+tab" {
				delta = -1
			}
			o.field += delta
			if o.kind == "roots" {
				if o.field >= len(o.fields) {
					o.field = -1
				}
				if o.field < -1 {
					o.field = len(o.fields) - 1
				}
			} else {
				o.field = (o.field + len(o.fields)) % len(o.fields)
			}
			if o.field >= 0 {
				return o.fields[o.field].Focus()
			}
			return nil
		case "enter":
			return m.applyFields()
		case "ctrl+n":
			if o.kind == "roots" {
				o.fields[o.field].Blur()
				o.fields = append(o.fields, field("Root", ""))
				o.field = len(o.fields) - 1
				return o.fields[o.field].Focus()
			}
		}
		if o.kind == "filters" && o.field >= 8 {
			return nil
		}
		var cmd tea.Cmd
		o.fields[o.field], cmd = o.fields[o.field].Update(k)
		return cmd
	}
	cs := o.visible()
	switch key {
	case "up":
		o.selected = max(0, o.selected-1)
		return nil
	case "down":
		o.selected = min(max(0, len(cs)-1), o.selected+1)
		return nil
	case "tab", "shift+tab":
		if len(o.fields) > 0 {
			o.field = 0
			return o.fields[0].Focus()
		}
		return nil
	case " ":
		if o.kind == "sources" && o.selected < len(cs) {
			for n := range o.choices {
				if o.choices[n].value == cs[o.selected].value {
					o.choices[n].checked = !o.choices[n].checked
				}
			}
			return nil
		}
	case "enter":
		return m.activateChoice()
	case "ctrl+p", "ctrl+d", "ctrl+r":
		if o.kind == "history" && o.selected < len(cs) {
			id := cs[o.selected].value
			cfg := m.cfg
			gen := m.historyGen
			if key == "ctrl+d" {
				return m.confirmDelete()
			}
			if key == "ctrl+r" {
				return func() tea.Msg {
					r, e := history.New(cfg).Get(m.ctx, id)
					return historyRerunMsg{gen: gen, run: r, err: e}
				}
			}
			pinned := false
			for _, r := range o.runs {
				if r.ID == id {
					pinned = r.Pinned
				}
			}
			return func() tea.Msg {
				e := history.New(cfg).Pin(m.ctx, id, !pinned)
				if e != nil {
					return noticeMsg(e.Error())
				}
				rs, e := history.New(cfg).List(m.ctx, 0)
				return historyMsg{gen: gen, runs: rs, err: e}
			}
		}
	}
	if o.kind == "actions" || o.kind == "history" || o.kind == "help" {
		old := o.input.Value()
		var cmd tea.Cmd
		o.input, cmd = o.input.Update(k)
		if o.input.Value() != old {
			o.selected = 0
		}
		return cmd
	}
	if key == "j" {
		o.selected = min(max(0, len(cs)-1), o.selected+1)
	}
	if key == "k" {
		o.selected = max(0, o.selected-1)
	}
	return nil
}

type historyRerunMsg struct {
	gen int
	run domain.Run
	err error
}

func (m *Model) activateChoice() tea.Cmd {
	o := m.overlay
	cs := o.visible()
	if o.kind == "sources" {
		var ss []string
		for _, c := range o.choices {
			if c.checked {
				ss = append(ss, c.value)
			}
		}
		if len(ss) == 0 {
			m.status = "Select at least one source"
			return nil
		}
		m.query.Sources = ss
		m.overlay = nil
		return m.changedScope()
	}
	if o.selected < 0 || o.selected >= len(cs) {
		return nil
	}
	c := cs[o.selected]
	if c.disabled {
		m.status = "This option is unavailable"
		return nil
	}
	switch o.kind {
	case "completion":
		return m.acceptCompletion()
	case "help":
		return nil
	case "copy":
		return m.copyChoice(c.value)
	case "usage":
		if c.value == "cancel" {
			m.cancelUsage()
			m.overlay = nil
			return nil
		}
		items := o.usageItems
		if strings.HasSuffix(c.value, "visible") {
			items = o.usageVisible
		}
		return m.startUsage(items, strings.HasPrefix(c.value, "refresh-"))
	case "delete-history":
		if c.value == "cancel" {
			m.overlay = o.parent
			return nil
		}
		return m.deleteHistory(strings.TrimPrefix(c.value, "delete:"), o.parent)
	case "sort":
		if m.sort == c.value {
			m.desc = !m.desc
		} else {
			m.sort = c.value
			m.desc = false
		}
		m.overlay = nil
		m.rebuild()
		return nil
	case "actions":
		if c.disabled {
			m.status = "This action is unavailable"
			return nil
		}
		return m.execute(c.value)
	case "history":
		cfg := m.cfg
		gen := m.historyGen
		return func() tea.Msg {
			r, e := history.New(cfg).Get(m.ctx, c.value)
			return snapshotMsg{gen: gen, run: r, err: e}
		}
	case "roots":
		if strings.HasPrefix(c.value, "ssh-session:") {
			host := strings.TrimPrefix(c.value, "ssh-session:")
			if strings.HasPrefix(host, "-") || strings.ContainsAny(host, "\n\r\x00 \t") {
				m.status = "Invalid SSH alias"
				return nil
			}
			tool := m.cfg.Tools.SSH
			if tool == "" {
				tool = "ssh"
			}
			cmd := exec.CommandContext(m.ctx, tool, "--", host)
			m.overlay = nil
			m.pendingAction = true
			return tea.ExecProcess(cmd, func(e error) tea.Msg { return childMsg{e, false} })
		}
		var q domain.QuerySpec
		if e := json.Unmarshal([]byte(c.value), &q); e != nil {
			m.status = e.Error()
			return nil
		}
		m.query.Roots = q.Roots
		m.query.Target = q.Target
		m.overlay = nil
		return m.changedScope()
	}
	return nil
}
func (m *Model) changedScope() tea.Cmd {
	m.dirty = true
	if m.query.Target.Remote() {
		save := m.retire()
		m.gen++
		m.edit++
		m.searching = false
		m.status = "Scope changed — Enter to search remote"
		m.focus = 0
		return tea.Batch(save, m.input.Focus())
	}
	return m.start(false)
}
func (m *Model) applyFields() tea.Cmd {
	o := m.overlay
	if o.kind == "roots" {
		var roots []string
		for _, f := range o.fields[1:] {
			if f.Value() != "" {
				roots = append(roots, f.Value())
			}
		}
		if len(roots) == 0 {
			m.status = "At least one root is required"
			return nil
		}
		host := strings.TrimSpace(o.fields[0].Value())
		if strings.HasPrefix(host, "-") || strings.ContainsAny(host, "\n\r\x00 ") {
			m.status = "Enter one SSH alias without whitespace"
			return nil
		}
		m.query.Roots = roots
		m.query.Target = domain.Target{Host: host}
		m.overlay = nil
		return m.changedScope()
	}
	if o.kind == "filters" {
		// Read the current draft before replacing inline qualifiers with form
		// values. This also allows the form to turn off an invalid regex.
		keywordBase := m.query
		keywordBase.Regex = false
		draft, err := domain.ParseQuery(m.input.Value(), keywordBase)
		if err != nil {
			m.status = err.Error()
			return nil
		}
		vals := make([]string, len(o.fields))
		for i, f := range o.fields {
			vals[i] = strings.TrimSpace(f.Value())
		}
		var qualifiers []string
		for i, prefix := range []string{"type:", "ext:", "mtime:<", "after:", "before:", "size:>=", "size:<=", "depth:"} {
			if vals[i] != "" && !(i == 7 && vals[i] == "0") {
				qualifiers = append(qualifiers, prefix+vals[i])
			}
		}
		base := m.query
		base.Filters = domain.Filters{}
		base.BaseFilters = nil
		base.Raw = ""
		parsed, e := domain.ParseQuery(strings.Join(qualifiers, " "), base)
		if e != nil {
			m.status = e.Error()
			return nil
		}
		bools := make([]bool, 4)
		for i := range bools {
			b, e := strconv.ParseBool(vals[8+i])
			if e != nil {
				m.status = "Boolean fields use true or false"
				return nil
			}
			bools[i] = b
		}
		parsed.Filters.Hidden = bools[0]
		parsed.Filters.Ignored = bools[1]
		next := m.query
		next.Filters = parsed.Filters
		bf := parsed.Filters
		next.BaseFilters = &bf
		next.Regex = bools[2]
		next.FullPath = bools[3]
		next.Text = draft.Text
		if err := domain.ValidateQuery(next); err != nil {
			m.status = err.Error()
			return nil
		}
		m.query = next
		// Quoting protects a literal keyword that resembles a qualifier.
		m.input.SetValue(strconv.Quote(draft.Text))
		m.query.Raw = m.input.Value()
		m.overlay = nil
		return m.changedScope()
	}
	return nil
}
func rootLabel(q domain.QuerySpec) string {
	r := strings.Join(q.Roots, ", ")
	if r == "" {
		r = "."
	}
	return q.Target.ID() + " · " + filepath.Clean(r)
}
