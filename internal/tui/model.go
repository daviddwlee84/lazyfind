package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/history"
	"github.com/daviddwlee84/lazyfind/internal/preview"
	"github.com/daviddwlee84/lazyfind/internal/search"
	"github.com/daviddwlee84/lazyfind/internal/usage"
)

type debounceMsg int
type eventsMsg struct {
	gen     int
	events  []domain.Event
	channel <-chan domain.Event
}
type previewMsg struct {
	gen      int
	id, text string
	resolved actions.Resolved
	err      error
	doc      *preview.Document
}
type copiedMsg struct {
	gen       int
	id, value string
	err       error
}
type savedMsg struct {
	gen int
	id  string
	err error
}
type childMsg struct {
	err     error
	replace bool
}
type preparedMsg struct {
	gen     int
	id      string
	command *exec.Cmd
	mode    string
	err     error
}
type historyMsg struct {
	gen  int
	runs []domain.Run
	err  error
}
type snapshotMsg struct {
	gen int
	run domain.Run
	err error
}
type noticeMsg string
type choice struct {
	label, value string
	checked      bool
	disabled     bool
}
type overlay struct {
	kind, title              string
	choices                  []choice
	selected, offset         int
	input                    textinput.Model
	fields                   []textinput.Model
	field                    int
	runs                     []domain.Run
	parent                   *overlay
	replaceStart, replaceEnd int
	copyItem                 *domain.Item
	copyRoots                []string
	copyMatch                int
	usageItems, usageVisible []domain.Item
}
type Model struct {
	ctx                                    context.Context
	cfg                                    config.Config
	query                                  domain.QuerySpec
	input, filter                          textinput.Model
	focus                                  int // 0=query, 1=results, 2=preview, 3=result filter
	width, height                          int
	items                                  map[string]domain.Item
	rows                                   []domain.Item
	selected, offset                       int
	selection                              string
	matchItemID                            string
	matchIndex                             int
	previewBody, previewOverride           string
	previewDoc                             *preview.Document
	actionSvc                              *actions.Service
	previewSvc                             *preview.Service
	sort                                   string
	desc                                   bool
	run                                    domain.Run
	parentID, nextParentID                 string
	historyGen, rootsGen                   int
	gen, edit, previewGen                  int
	cancel, previewCancel                  context.CancelFunc
	searching, accepted, historical, dirty bool
	status                                 string
	viewport                               viewport.Model
	resolved                               actions.Resolved
	overlay                                *overlay
	mouse                                  bool
	pressed                                string
	pick                                   bool
	picked                                 *domain.Item
	pendingAction                          bool
	pendingG                               bool
	actionsGen                             int
	usageSvc                               *usage.Service
	usageValues                            map[string]usage.Result
	usageCancel                            context.CancelFunc
	usageGen, usageDone, usageTotal        int
	usageRunning                           bool
	panel                                  bool
}

func New(ctx context.Context, cfg config.Config, q domain.QuerySpec, pick bool) *Model {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "Search a keyword, then refine…"
	in.CharLimit = 4096
	in.SetValue(q.Raw)
	in.Focus()
	f := textinput.New()
	f.Prompt = "Filter results: "
	f.Placeholder = "fuzzy match current list"
	f.CharLimit = 1024
	v := viewport.New()
	v.MouseWheelEnabled = false
	v.SoftWrap = true
	m := &Model{ctx: ctx, cfg: cfg, actionSvc: actions.New(cfg), previewSvc: preview.New(cfg), usageSvc: usage.New(cfg), usageValues: map[string]usage.Result{}, query: q, input: in, filter: f, items: map[string]domain.Item{}, sort: "name", width: 100, height: 30, viewport: v, mouse: cfg.UI.Mouse, pick: pick, status: "Type a keyword • names + text"}
	if cfg.UI.InitialFocus == "results" {
		m.focus = 1
		m.input.Blur()
	}
	return m
}
func Run(ctx context.Context, cfg config.Config, q domain.QuerySpec, pick bool) (*domain.Item, error) {
	m := New(ctx, cfg, q, pick)
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(os.Stderr))
	_, err := p.Run()
	m.cancelUsage()
	if m.cancel != nil {
		m.cancel()
	}
	if m.previewCancel != nil {
		m.previewCancel()
	}
	if m.accepted && !m.historical && m.run.ID != "" && cfg.History.Enabled {
		r := m.finalSnapshot()
		c, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if e := history.New(cfg).Save(c, r); e != nil {
			fmt.Fprintln(os.Stderr, "History was not saved:", e)
		}
	}
	return m.picked, err
}
func (m *Model) Init() tea.Cmd {
	if m.query.Target.Remote() {
		m.dirty = true
		return textinput.Blink
	}
	return tea.Batch(textinput.Blink, m.start(false))
}
func waitEvents(gen int, ch <-chan domain.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		ev := []domain.Event{e}
		for len(ev) < 128 {
			select {
			case e, ok = <-ch:
				if !ok {
					return eventsMsg{gen, ev, nil}
				}
				ev = append(ev, e)
			default:
				return eventsMsg{gen, ev, ch}
			}
		}
		return eventsMsg{gen, ev, ch}
	}
}
func (m *Model) start(accept bool) tea.Cmd {
	q, err := domain.ParseQuery(m.input.Value(), m.query)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	if !accept && !m.cfg.Search.AutoSearchEmpty && !domain.HasSearchIntent(q) {
		return m.idleQuery(q)
	}
	m.cancelUsage()
	parentID := m.nextParentID
	if parentID == "" && m.historical {
		parentID = m.run.ID
	}
	m.nextParentID = ""
	save := m.retire()
	m.gen++
	gen := m.gen
	m.parentID = parentID
	m.edit++
	m.query = q
	m.dirty = false
	m.historical = false
	m.searching = true
	m.accepted = accept
	m.items = map[string]domain.Item{}
	m.rows = nil
	m.selected = 0
	m.offset = 0
	m.selection = ""
	m.matchItemID = ""
	m.matchIndex = 0
	m.previewOverride = ""
	m.previewBody = ""
	m.previewDoc = nil
	m.run = domain.Run{}
	m.status = "Searching…"
	m.resolved = actions.Resolved{}
	m.previewGen++
	if m.previewCancel != nil {
		m.previewCancel()
	}
	m.viewport.SetContent("Waiting for results…")
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	ch := make(chan domain.Event, 256)
	cfg := m.cfg
	go func() {
		defer close(ch)
		r := search.New(cfg).Execute(ctx, q, func(e domain.Event) {
			select {
			case ch <- e:
			case <-ctx.Done():
			}
		})
		finishEvents(ctx, ch, domain.Event{Kind: "done", Run: &r})
	}()
	return tea.Batch(save, waitEvents(gen, ch))
}

// finishEvents keeps normal completion ordered after streamed items. Once
// canceled, intermediate events may be discarded, but a final event is always
// buffered for the current consumer. Abandoned generations never block a worker.
func finishEvents(ctx context.Context, ch chan domain.Event, done domain.Event) {
	select {
	case ch <- done:
		return
	case <-ctx.Done():
	}
	for {
		select {
		case ch <- done:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}

func (m *Model) snapshot() domain.Run {
	r := m.run
	r.Items = make([]domain.Item, 0, len(m.items))
	for _, i := range m.items {
		r.Items = append(r.Items, i)
	}
	domain.SortItems(r.Items, m.sort, m.desc)
	r.Observed = max(r.Observed, len(r.Items))
	r.CapturedAt = time.Now()
	return r
}

func (m *Model) finalSnapshot() domain.Run {
	r := m.snapshot()
	if m.searching || r.Status == "running" {
		r.Status = "canceled"
		finished := r.CapturedAt
		r.FinishedAt = &finished
	}
	return r
}

// retire captures a confirmed generation before its stream is abandoned.
// Historical snapshots are immutable: navigation and actions never resave them.
func (m *Model) retire() tea.Cmd {
	m.cancelUsage()
	var cmd tea.Cmd
	if m.accepted && !m.historical && m.run.ID != "" {
		r := m.finalSnapshot()
		m.run = r
		cmd = m.persist(r)
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.searching = false
	m.accepted = false
	m.pendingAction = false
	return cmd
}
func (m *Model) save() tea.Cmd {
	if !m.accepted || m.run.ID == "" || m.historical {
		return nil
	}
	return m.persist(m.snapshot())
}
func (m *Model) persist(r domain.Run) tea.Cmd {
	if !m.cfg.History.Enabled || r.ID == "" {
		return nil
	}
	cfg := m.cfg
	gen := m.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s := history.New(cfg)
		err := s.Save(ctx, r)
		return savedMsg{gen: gen, id: r.ID, err: err}
	}
}
func (m *Model) accept() tea.Cmd {
	if m.historical {
		return nil
	}
	m.accepted = true
	return m.save()
}
func (m *Model) submit() tea.Cmd {
	if m.dirty || (m.run.ID == "" && !m.searching) {
		return m.start(true)
	}
	return m.accept()
}
func (m *Model) current() *domain.Item {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return nil
	}
	i := m.rows[m.selected]
	return &i
}
func (m *Model) rebuild() {
	old := m.selection
	m.rows = m.rows[:0]
	for _, i := range m.items {
		if domain.Fuzzy(i.Path, m.filter.Value()) {
			m.rows = append(m.rows, i)
		}
	}
	m.sortRows()
	m.selected = min(m.selected, max(0, len(m.rows)-1))
	for n, i := range m.rows {
		if i.ID == old {
			m.selected = n
			break
		}
	}
	if i := m.current(); i != nil {
		m.selection = i.ID
	} else {
		m.selection = ""
	}
	m.syncMatchSelection()
	m.clamp()
}
func (m *Model) clamp() {
	h := m.layout().bodyHeight
	if m.selected < m.offset {
		m.offset = m.selected
	}
	if m.selected >= m.offset+h {
		m.offset = m.selected - h + 1
	}
	m.offset = max(0, min(m.offset, max(0, len(m.rows)-h)))
}
func (m *Model) selectRow(n int) tea.Cmd {
	m.selected = max(0, min(n, len(m.rows)-1))
	if i := m.current(); i != nil {
		m.selection = i.ID
	}
	m.syncMatchSelection()
	m.clamp()
	m.pressed = ""
	return m.loadPreview()
}
func (m *Model) loadPreview() tea.Cmd { return m.loadPreviewAction("", false) }

func (m *Model) loadPreviewAction(override string, allowHistorical bool) tea.Cmd {
	m.previewDoc = nil
	m.previewGen++
	gen := m.previewGen
	if m.previewCancel != nil {
		m.previewCancel()
	}
	m.resolved = actions.Resolved{}
	m.syncMatchSelection()
	m.previewOverride = override
	item := m.actionItem()
	if item == nil {
		m.previewBody = "No result selected."
		m.viewport.SetContent(m.previewBody)
		return nil
	}
	m.viewport.GotoTop()
	if m.historical && !allowHistorical {
		m.previewDoc = &preview.Document{Metadata: fmt.Sprintf("Historical snapshot — %s\n%s", m.run.CapturedAt.Format(time.RFC3339), domain.Display(item.Path)), Snippets: item.Matches, Opaque: "Copy references offline with y.\nCtrl+R searches again."}
		m.refreshPreview()
		return nil
	}
	m.previewBody = "Loading preview…"
	m.viewport.SetContent(m.decoratePreview(m.previewBody))
	ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
	m.previewCancel = cancel
	q := m.query.Text
	actionSvc, previewSvc := m.actionSvc, m.previewSvc
	readPreview := m.cfg.UI.Preview || override != ""
	return func() tea.Msg {
		defer cancel()
		if allowHistorical {
			if err := actionSvc.Exists(ctx, *item); err != nil {
				return previewMsg{gen: gen, id: item.ID, err: err}
			}
		}
		resolved, err := actionSvc.Resolve(ctx, *item, q)
		if err != nil {
			return previewMsg{gen: gen, id: item.ID, resolved: resolved, err: err}
		}
		if override != "" {
			resolved.Preview = override
		}
		if !readPreview {
			return previewMsg{gen: gen, id: item.ID, text: "Preview hidden. Toggle preview to load file contents.", resolved: resolved}
		}
		doc, err := previewSvc.LoadDocument(ctx, *item, q, resolved)
		return previewMsg{gen: gen, id: item.ID, doc: &doc, resolved: resolved, err: err}
	}
}

// Match navigation changes view state only. It never edits stored item ordering.
func (m *Model) syncMatchSelection() {
	item := m.current()
	if item == nil {
		m.matchItemID = ""
		m.matchIndex = 0
		m.previewOverride = ""
		return
	}
	if m.matchItemID != item.ID {
		m.matchItemID = item.ID
		m.matchIndex = 0
		m.previewOverride = ""
	}
	m.matchIndex = max(0, min(m.matchIndex, len(item.Matches)-1))
}
func (m *Model) actionItem() *domain.Item {
	item := m.current()
	if item == nil {
		return nil
	}
	if len(item.Matches) == 0 {
		return item
	}
	index := max(0, min(m.matchIndex, len(item.Matches)-1))
	matches := make([]domain.Match, 0, len(item.Matches))
	matches = append(matches, item.Matches[index])
	matches = append(matches, item.Matches[:index]...)
	matches = append(matches, item.Matches[index+1:]...)
	item.Matches = matches
	return item
}
func (m *Model) matchHeader() string {
	item := m.current()
	if item == nil {
		return ""
	}
	if len(item.Matches) == 0 {
		return "Sources: " + strings.Join(item.Sources, ", ") + " · no content matches"
	}
	index := max(0, min(m.matchIndex, len(item.Matches)-1))
	hit := item.Matches[index]
	locator := "line"
	if hit.Extracted {
		locator = "extracted line"
	}
	column := ""
	if hit.Column > 0 && !hit.Extracted {
		column = fmt.Sprintf(":%d", hit.Column)
	}
	more := ""
	if item.MatchesTruncated {
		more = " saved (more found)"
	}
	return fmt.Sprintf("Match %d/%d%s · %s %s %d%s", index+1, len(item.Matches), more, hit.Source, locator, hit.Line, column)
}
func (m *Model) decoratePreview(body string) string {
	header := m.matchHeader()
	if header == "" {
		return body
	}
	item := m.current()
	if item != nil && len(item.Matches) > 0 {
		index := max(0, min(m.matchIndex, len(item.Matches)-1))
		text, spans := domain.ClipSpans(item.Matches[index].Text, item.Matches[index].Spans, 512)
		text = m.highlight(text, spans, false)
		if text != "" {
			header += "\n" + text
		}
	}
	return header + "\n\n" + body
}
func (m *Model) navigateMatch(delta int) tea.Cmd {
	m.syncMatchSelection()
	item := m.current()
	if item == nil || len(item.Matches) == 0 {
		return nil
	}
	m.matchIndex = (m.matchIndex + delta + len(item.Matches)) % len(item.Matches)
	override := m.previewOverride
	return m.loadPreviewAction(override, m.historical && override != "")
}
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case usageMsg:
		return m, m.receiveUsage(v)
	case deletedMsg:
		return m, m.historyDeleted(v)
	case actionsMsg:
		target := m.overlay
		for target != nil && target.kind != "actions" {
			target = target.parent
		}
		if target == nil || v.gen != m.gen || v.dialog != m.actionsGen || v.id != m.selection {
			return m, nil
		}
		if v.err != nil {
			m.status = v.err.Error()
			return m, nil
		}
		m.resolved = v.resolved
		target.choices = m.commonActions()
		for _, a := range v.resolved.Actions {
			label := a.Label
			if !a.Available {
				label += " — " + a.Reason
			}
			target.choices = append(target.choices, choice{label: label, value: a.ID, disabled: !a.Available})
		}
		return m, nil
	case rootsMsg:
		if m.overlay != nil && m.overlay.kind == "roots" && v.gen == m.rootsGen {
			m.overlay.choices = v.choices
			if len(v.problems) > 0 {
				m.status = strings.Join(v.problems, "; ")
			}
		}
		return m, nil
	case historyRerunMsg:
		if m.overlay == nil || m.overlay.kind != "history" || v.gen != m.historyGen {
			return m, nil
		}
		if v.err != nil {
			m.status = v.err.Error()
			return m, nil
		}
		m.overlay = nil
		m.query = v.run.Query
		m.input.SetValue(v.run.Query.Raw)
		m.focus = 1
		m.input.Blur()
		m.nextParentID = v.run.ID
		return m, m.start(true)
	case tea.WindowSizeMsg:
		m.width = max(1, v.Width)
		m.height = max(1, v.Height)
		m.pressed = ""
		m.resize()
		return m, nil
	case debounceMsg:
		if int(v) == m.edit && !m.query.Target.Remote() && m.dirty {
			return m, m.start(false)
		}
		return m, nil
	case eventsMsg:
		if v.gen != m.gen {
			return m, nil
		}
		old := m.selection
		var cmds []tea.Cmd
		for _, e := range v.events {
			switch e.Kind {
			case "start", "scope":
				if e.Run != nil {
					m.run = *e.Run
					m.run.ParentID = m.parentID
					if e.Kind == "scope" {
						m.query.Roots = append([]string(nil), e.Run.Query.Roots...)
					}
					if m.accepted {
						cmds = append(cmds, m.save())
					}
				}
			case "item":
				if e.Item != nil {
					m.items[e.Item.ID] = *e.Item
				}
			case "problem":
				if e.Problem != nil {
					m.status = e.Problem.Message
					m.run.Problems = append(m.run.Problems, *e.Problem)
				}
			case "done":
				if e.Run != nil {
					m.run = *e.Run
					m.run.ParentID = m.parentID
					m.items = map[string]domain.Item{}
					for _, i := range e.Run.Items {
						m.items[i.ID] = i
					}
					m.searching = false
					m.status = fmt.Sprintf("%s · %d results", m.run.Status, len(m.items))
					if len(m.run.Problems) > 0 {
						m.status += " · " + m.run.Problems[0].Message
					}
					if m.accepted {
						cmds = append(cmds, m.save())
					}
				}
			}
		}
		m.rebuild()
		if old != m.selection || !m.searching {
			cmds = append(cmds, m.loadPreview())
		}
		if v.channel != nil {
			cmds = append(cmds, waitEvents(v.gen, v.channel))
		}
		return m, tea.Batch(cmds...)
	case previewMsg:
		if v.gen == m.previewGen && v.id == m.selection {
			m.resolved = v.resolved
			if v.err != nil {
				v.text += "\n" + v.err.Error()
			}
			m.previewBody = v.text
			m.previewDoc = v.doc
			m.refreshPreview()
		}
		return m, nil
	case copiedMsg:
		if v.gen != m.gen {
			return m, nil
		}
		m.pendingAction = false
		if v.id != m.selection {
			return m, nil
		}
		if v.err != nil {
			m.status = "Copy failed: " + v.err.Error()
			return m, nil
		}
		m.status = "Copied " + domain.Display(v.value)
		return m, tea.SetClipboard(v.value)
	case savedMsg:
		if v.gen == m.gen && v.id == m.run.ID && v.err != nil {
			m.status = "History not saved: " + v.err.Error()
		}
		return m, nil
	case noticeMsg:
		m.status = string(v)
		return m, nil
	case historyMsg:
		if m.overlay != nil && m.overlay.kind == "history" && v.gen == m.historyGen {
			if v.err != nil {
				m.status = v.err.Error()
			} else {
				m.overlay.runs = v.runs
				m.overlay.choices = nil
				for _, r := range v.runs {
					pin := ""
					if r.Pinned {
						pin = "★ "
					}
					m.overlay.choices = append(m.overlay.choices, choice{label: fmt.Sprintf("%s%s  %s  %s", pin, r.StartedAt.Format("01-02 15:04"), r.Query.Target.ID(), r.Query.Raw), value: r.ID})
				}
			}
		}
		return m, nil
	case snapshotMsg:
		if m.overlay == nil || m.overlay.kind != "history" || v.gen != m.historyGen {
			return m, nil
		}
		if v.err != nil {
			m.status = v.err.Error()
			return m, nil
		}
		save := m.retire()
		m.gen++
		m.edit++
		m.searching = false
		m.dirty = false
		m.overlay = nil
		m.run = v.run
		m.query = v.run.Query
		m.input.SetValue(v.run.Query.Raw)
		m.items = map[string]domain.Item{}
		for _, i := range v.run.Items {
			m.items[i.ID] = i
		}
		m.filter.SetValue("")
		m.historical = true
		m.accepted = false
		m.focus = 1
		m.input.Blur()
		m.rebuild()
		m.status = "Historical snapshot · " + v.run.CapturedAt.Format(time.RFC3339) + " · " + v.run.Status
		return m, tea.Batch(save, m.loadPreview())
	case preparedMsg:
		if v.gen != m.gen {
			return m, nil
		}
		m.pendingAction = false
		if v.err != nil {
			m.status = v.err.Error()
			return m, nil
		}
		if v.id != m.selection {
			m.status = "Selection changed; action canceled"
			return m, nil
		}
		m.pendingAction = true
		if v.mode == "detach" {
			return m, func() tea.Msg {
				err := v.command.Start()
				if err == nil {
					go v.command.Wait()
				}
				return childMsg{err, false}
			}
		}
		return m, tea.ExecProcess(v.command, func(e error) tea.Msg { return childMsg{e, v.mode == "replace"} })
	case childMsg:
		m.pendingAction = false
		if item := m.current(); item != nil {
			m.previewSvc.Invalidate(*item)
		}
		if v.err != nil {
			m.status = "Action failed: " + v.err.Error()
		} else {
			m.status = "Action finished"
		}
		if v.replace && v.err == nil {
			return m, tea.Quit
		}
		return m, m.loadPreview()
	case tea.MouseClickMsg:
		return m, m.mouseClick(v.Mouse())
	case tea.MouseReleaseMsg:
		return m, m.mouseRelease(v.Mouse())
	case tea.MouseWheelMsg:
		return m, m.mouseWheel(v.Mouse())
	case tea.KeyPressMsg:
		return m, m.key(v)
	}
	var cmd tea.Cmd
	if m.overlay != nil {
		if m.overlay.kind == "completion" {
			old := m.input.Value()
			m.input, cmd = m.input.Update(msg)
			return m, m.queryEdited(old, cmd)
		}
		if len(m.overlay.fields) > 0 {
			f := m.overlay.field
			if f >= 0 && f < len(m.overlay.fields) {
				m.overlay.fields[f], cmd = m.overlay.fields[f].Update(msg)
			}
		} else {
			before := m.overlay.input.Value()
			m.overlay.input, cmd = m.overlay.input.Update(msg)
			if before != m.overlay.input.Value() {
				m.overlay.selected = 0
			}
		}
	} else if m.focus == 0 {
		old := m.input.Value()
		m.input, cmd = m.input.Update(msg)
		cmd = m.queryEdited(old, cmd)
	} else if m.focus == 3 {
		before := m.selection
		m.filter, cmd = m.filter.Update(msg)
		m.rebuild()
		if before != m.selection {
			cmd = tea.Batch(cmd, m.loadPreview())
		}
	}
	return m, cmd
}

// queryEdited covers typing, bracketed paste and clipboard paste uniformly.
func (m *Model) queryEdited(old string, cmd tea.Cmd) tea.Cmd {
	if old == m.input.Value() {
		return cmd
	}
	m.edit++
	m.dirty = true
	if m.overlay == nil || m.overlay.kind == "completion" {
		m.showCompletion(false)
	}
	if !m.cfg.Search.AutoSearchEmpty {
		if q, e := domain.ParseQuery(m.input.Value(), m.query); e == nil && !domain.HasSearchIntent(q) {
			return tea.Batch(cmd, m.idleQuery(q))
		}
	}
	n := m.edit
	if !m.query.Target.Remote() {
		return tea.Batch(cmd, tea.Tick(time.Duration(m.cfg.Search.DebounceMS)*time.Millisecond, func(time.Time) tea.Msg { return debounceMsg(n) }))
	}
	m.status = "Draft changed — Enter to search remote"
	return cmd
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key == "ctrl+c" {
		if m.overlay != nil {
			m.overlay = nil
			return nil
		}
		if m.searching {
			m.cancel()
			m.status = "Canceling…"
			return nil
		}
		if m.usageRunning {
			m.cancelUsage()
			m.status = "Directory usage canceled"
			return nil
		}
		return tea.Quit
	}
	if m.overlay != nil {
		if m.overlay.kind == "completion" {
			return m.completionKey(k)
		}
		return m.overlayKey(k)
	}
	if m.focus == 0 || m.focus == 3 {
		if m.focus == 0 && matchesKey(key, m.cfg.Keymap["complete_query"]) {
			return m.showCompletion(true)
		}
		switch key {
		case "up":
			return m.selectRow(m.selected - 1)
		case "down":
			return m.selectRow(m.selected + 1)
		case "enter", "tab":
			old := m.focus
			m.focus = 1
			m.input.Blur()
			m.filter.Blur()
			if old == 0 {
				if key == "tab" && (m.query.Target.Remote() || (!m.cfg.Search.AutoSearchEmpty && m.run.ID == "" && !m.searching)) {
					return nil
				}
				return m.submit()
			}
			return nil
		case "shift+tab":
			m.focus = 2
			m.input.Blur()
			m.filter.Blur()
			return nil
		case "esc":
			before := m.selection
			if m.focus == 3 {
				m.filter.SetValue("")
				m.rebuild()
			}
			m.focus = 1
			m.input.Blur()
			m.filter.Blur()
			if before != m.selection {
				return m.loadPreview()
			}
			return nil
		}
		var cmd tea.Cmd
		if m.focus == 3 {
			before := m.selection
			m.filter, cmd = m.filter.Update(k)
			m.rebuild()
			if before != m.selection {
				return tea.Batch(cmd, m.loadPreview())
			}
			return cmd
		}
		old := m.input.Value()
		m.input, cmd = m.input.Update(k)
		return m.queryEdited(old, cmd)
	}
	if key == "tab" || key == "shift+tab" {
		if key == "tab" {
			m.focus = (m.focus + 1) % 3
		} else {
			m.focus = (m.focus + 2) % 3
		}
		if m.focus == 0 {
			return m.input.Focus()
		}
		return nil
	}
	if key == "esc" {
		m.pendingG = false
		if m.searching {
			m.cancel()
		}
		if m.usageRunning {
			m.cancelUsage()
			m.status = "Directory usage canceled"
		}
		return nil
	}
	if key == "enter" {
		if m.pick {
			if i := m.current(); i != nil {
				m.picked = i
				return tea.Batch(m.accept(), tea.Quit)
			}
			return nil
		}
		if m.historical || m.resolved.Default == "" {
			return m.showActions()
		}
		return m.execute(m.resolved.Default)
	}
	for id, binding := range m.cfg.Keymap {
		if matchesKey(key, binding) {
			return m.command(id)
		}
	}
	if m.focus == 2 {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(k)
		return cmd
	}
	switch key {
	case "up", "k":
		return m.selectRow(m.selected - 1)
	case "down", "j":
		return m.selectRow(m.selected + 1)
	case "home":
		return m.selectRow(0)
	case "end", "G":
		return m.selectRow(len(m.rows) - 1)
	case "pgdown":
		return m.selectRow(m.selected + m.layout().bodyHeight)
	case "pgup":
		return m.selectRow(m.selected - m.layout().bodyHeight)
	case "g":
		if m.pendingG {
			m.pendingG = false
			return m.selectRow(0)
		}
		m.pendingG = true
		return nil
	case "left", "h":
		m.focus = 0
		return m.input.Focus()
	case "right", "l":
		m.focus = 2
		return nil
	}
	m.pendingG = false
	for _, a := range m.resolved.Actions {
		if a.Key != "" && key == a.Key && a.Available {
			return m.execute(a.ID)
		}
	}
	return nil
}
func (m *Model) command(id string) tea.Cmd {
	m.pressed = ""
	switch id {
	case "quit":
		return tea.Quit
	case "search", "insert_search":
		m.focus = 0
		return m.input.Focus()
	case "next_match":
		return m.navigateMatch(1)
	case "previous_match":
		return m.navigateMatch(-1)
	case "result_filter":
		m.focus = 3
		return m.filter.Focus()
	case "preview_lines":
		m.cfg.UI.PreviewLineNumbers = !m.cfg.UI.PreviewLineNumbers
		m.refreshPreview()
		return nil
	case "copy_menu":
		return m.showCopy()
	case "directory_usage":
		return m.showUsage()
	case "preview":
		m.cfg.UI.Preview = !m.cfg.UI.Preview
		m.resize()
		return m.loadPreview()
	case "mouse":
		m.mouse = !m.mouse
		return nil
	case "refresh":
		m.nextParentID = m.run.ID
		return m.start(true)
	case "actions":
		return m.showActions()
	case "roots":
		return m.showRoots()
	case "filters":
		return m.showFilters()
	case "sources":
		m.showSources()
	case "history":
		m.historyGen++
		m.overlay = &overlay{kind: "history", title: "History · Enter view · Ctrl+R rerun · Ctrl+P pin · Ctrl+D delete", input: field("Find history", "")}
		m.overlay.input.Focus()
		return m.historyList()
	case "sort":
		m.overlay = &overlay{kind: "sort", title: "Sort · Enter chooses / reverses current column"}
		for _, s := range []string{"name", "path", "kind", "extension", "size", "modified", "usage"} {
			m.overlay.choices = append(m.overlay.choices, choice{label: s, value: s, checked: s == m.sort})
		}
	case "help":
		return m.showHelp()
	}
	return nil
}
func (m *Model) execute(id string) tea.Cmd {
	if id == "copy-menu" {
		return m.showCopy()
	}
	if id == "directory-usage" {
		return m.showUsage()
	}
	if id == "load-actions" {
		return m.resolveActions()
	}
	if m.pendingAction {
		return nil
	}
	m.syncMatchSelection()
	item := m.actionItem()
	if item == nil {
		return nil
	}
	m.overlay = nil
	svc := m.actionSvc
	action, knownAction := m.resolved.Find(id)
	if id == "copy" && (!knownAction || len(action.Argv) == 0) {
		m.pendingAction = true
		gen := m.gen
		return tea.Batch(m.accept(), func() tea.Msg {
			return copiedMsg{gen: gen, id: item.ID, value: actions.CopyPath(*item)}
		})
	}
	mode := "suspend"
	if knownAction {
		mode = action.Mode
	}
	if mode == "preview" {
		m.focus = 2
		return tea.Batch(m.accept(), m.loadPreviewAction(id, true))
	}
	m.pendingAction = true
	q := m.query.Text
	gen := m.gen
	return tea.Batch(m.accept(), func() tea.Msg {
		cmd, err := svc.Prepare(m.ctx, *item, q, id)
		return preparedMsg{gen: gen, id: item.ID, command: cmd, mode: mode, err: err}
	})
}
func (m *Model) historyList() tea.Cmd {
	cfg := m.cfg
	gen := m.historyGen
	return func() tea.Msg {
		r, e := history.New(cfg).List(context.Background(), 0)
		return historyMsg{gen: gen, runs: r, err: e}
	}
}
