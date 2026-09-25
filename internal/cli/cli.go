package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/daviddwlee84/lazyfind/internal/managedupgrade"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/actions"
	"github.com/daviddwlee84/lazyfind/internal/cache"
	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/history"
	"github.com/daviddwlee84/lazyfind/internal/hosts"
	"github.com/daviddwlee84/lazyfind/internal/search"
	"github.com/daviddwlee84/lazyfind/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }
func (e exitError) Unwrap() error { return e.err }
func ExitCode(e error) int {
	var c exitError
	if errors.As(e, &c) {
		return c.code
	}
	if errors.Is(e, context.Canceled) {
		return 130
	}
	return 1
}
func usage(e error) error {
	if e == nil {
		return nil
	}
	return exitError{2, e}
}

type options struct {
	configPath, query, host                     string
	roots, sources                              []string
	regex, fullPath, hidden, ignored, noHistory bool
}

func (o *options) load(cmd *cobra.Command) (config.Config, domain.QuerySpec, error) {
	cfg, e := config.Load(o.configPath)
	if e != nil {
		return cfg, domain.QuerySpec{}, usage(e)
	}
	q := domain.QuerySpec{Raw: o.query, Roots: o.roots, Target: domain.Target{Host: o.host}, Sources: cfg.Search.Sources, Regex: o.regex, FullPath: o.fullPath, Filters: domain.Filters{Hidden: cfg.Search.Hidden, Ignored: cfg.Search.Ignored}}
	if cmd.Flags().Changed("source") {
		q.Sources = o.sources
		if len(q.Sources) == 0 {
			return cfg, q, usage(errors.New("--source requires at least one source"))
		}
	}
	if cmd.Flags().Changed("hidden") {
		q.Filters.Hidden = o.hidden
	}
	if cmd.Flags().Changed("ignored") {
		q.Filters.Ignored = o.ignored
	}
	if o.noHistory {
		cfg.History.Enabled = false
	}
	q, e = domain.ParseQuery(o.query, q)
	return cfg, q, usage(e)
}
func addSearchFlags(c *cobra.Command, o *options) {
	f := c.Flags()
	f.StringVar(&o.query, "query", "", "Keyword and optional qualifiers")
	f.StringArrayVar(&o.roots, "root", nil, "Search root (repeatable; same target)")
	f.StringVar(&o.host, "host", "", "OpenSSH alias (empty uses local machine)")
	f.StringSliceVar(&o.sources, "source", nil, "Sources: names,text,documents,recent")
	f.BoolVar(&o.regex, "regex", false, "Treat keyword as regular expression")
	f.BoolVar(&o.fullPath, "full-path", false, "Match full paths instead of basenames")
	f.BoolVar(&o.hidden, "hidden", false, "Include hidden entries")
	f.BoolVar(&o.ignored, "ignored", false, "Include ignored entries")
	f.BoolVar(&o.noHistory, "no-history", false, "Do not persist this interactive search")
}
func New(version string) *cobra.Command {
	o := &options{}
	root := &cobra.Command{Use: "lazyfind [ROOT...]", Args: cobra.ArbitraryArgs, Short: "Find, inspect and act across local files and SSH targets", Version: version, SilenceUsage: true, SilenceErrors: true}
	root.SetFlagErrorFunc(func(c *cobra.Command, e error) error { return usage(e) })
	root.PersistentFlags().StringVar(&o.configPath, "config", "", "Configuration file (default: XDG config)")
	addSearchFlags(root, o)
	root.RunE = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			o.roots = append(o.roots, args...)
		}
		cfg, q, e := o.load(c)
		if e != nil {
			return e
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
			return usage(errors.New("TUI requires a terminal; use lazyfind search QUERY --json for scripts"))
		}
		_, e = tui.Run(c.Context(), cfg, q, false)
		return e
	}
	sopt := &options{}
	var jsonOut, save bool
	sc := &cobra.Command{Use: "search [QUERY]", Short: "Search without opening the TUI", Args: cobra.MaximumNArgs(1)}
	addSearchFlags(sc, sopt)
	sc.Flags().BoolVar(&jsonOut, "json", false, "Emit a versioned JSON result")
	sc.Flags().BoolVar(&save, "save", false, "Save this run in history")
	sc.RunE = func(c *cobra.Command, args []string) error {
		sopt.configPath = o.configPath
		if len(args) > 0 {
			if c.Flags().Changed("query") {
				return usage(errors.New("supply QUERY or --query, not both"))
			}
			sopt.query = args[0]
		}
		cfg, q, e := sopt.load(c)
		if e != nil {
			return e
		}
		r := search.New(cfg).Execute(c.Context(), q, nil)
		if save && cfg.History.Enabled {
			if e := saveRun(c.Context(), cfg, r); e != nil {
				fmt.Fprintln(c.ErrOrStderr(), "History not saved:", e)
			}
		}
		if e := writeRun(c, r, jsonOut); e != nil {
			return e
		}
		return runError(r)
	}
	root.AddCommand(managedupgrade.NewCommand(managedupgrade.Product{Binary: "lazyfind", Module: "github.com/daviddwlee84/lazyfind", Main: "github.com/daviddwlee84/lazyfind"}))
	root.AddCommand(sc)
	popt := &options{}
	var print0, dirs bool
	pick := &cobra.Command{Use: "pick", Short: "Select one result; emit only its path on stdout", Args: cobra.NoArgs}
	addSearchFlags(pick, popt)
	pick.Flags().BoolVar(&print0, "print0", false, "Terminate the selected path with NUL")
	pick.Flags().BoolVar(&dirs, "directories", false, "Only select directories")
	pick.RunE = func(c *cobra.Command, args []string) error {
		popt.configPath = o.configPath
		cfg, q, e := popt.load(c)
		if e != nil {
			return e
		}
		if dirs {
			q.Filters.Kinds = []string{"directory"}
			bf := q.Filters
			q.BaseFilters = &bf
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
			return usage(errors.New("pick requires terminal input and stderr"))
		}
		i, e := tui.Run(c.Context(), cfg, q, true)
		if e != nil {
			return e
		}
		if i == nil {
			return exitError{130, errors.New("selection canceled")}
		}
		value := i.RawPath()
		if i.Target.Remote() {
			value = i.Target.Host + ":" + value
		}
		end := "\n"
		if print0 {
			end = "\x00"
		}
		_, e = fmt.Fprint(c.OutOrStdout(), value+end)
		return e
	}
	root.AddCommand(pick)
	root.AddCommand(historyCommand(o), configCommand(o), cacheCommand(o), doctorCommand(o))
	root.AddCommand(&cobra.Command{Use: "completion [bash|zsh|fish|powershell]", Short: "Generate shell completion", Args: cobra.ExactArgs(1), ValidArgs: []string{"bash", "zsh", "fish", "powershell"}, RunE: func(c *cobra.Command, a []string) error {
		switch a[0] {
		case "bash":
			return root.GenBashCompletion(c.OutOrStdout())
		case "zsh":
			return root.GenZshCompletion(c.OutOrStdout())
		case "fish":
			return root.GenFishCompletion(c.OutOrStdout(), true)
		case "powershell":
			return root.GenPowerShellCompletion(c.OutOrStdout())
		}
		return usage(errors.New("unsupported shell"))
	}})
	wrapArgumentErrors(root)
	return root
}
func wrapArgumentErrors(c *cobra.Command) {
	if validate := c.Args; validate != nil {
		c.Args = func(cmd *cobra.Command, args []string) error { return usage(validate(cmd, args)) }
	}
	for _, child := range c.Commands() {
		wrapArgumentErrors(child)
	}
}
func saveRun(ctx context.Context, cfg config.Config, r domain.Run) error {
	if !cfg.History.Enabled {
		return nil
	}
	s := history.New(cfg)
	if e := s.Save(ctx, r); e != nil {
		return e
	}
	return s.Prune(ctx)
}
func runError(r domain.Run) error {
	switch r.Status {
	case "failed", "partial":
		return errors.New("search " + r.Status + "; inspect problems for details")
	case "cancelled", "canceled":
		return exitError{130, errors.New("search canceled")}
	}
	return nil
}
func writeJSON(c *cobra.Command, v any) error {
	enc := json.NewEncoder(c.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func writeRun(c *cobra.Command, r domain.Run, j bool) error {
	if j {
		return writeJSON(c, r)
	}
	for _, i := range r.Items {
		prefix := ""
		if i.Target.Remote() {
			prefix = i.Target.Host + ":"
		}
		fmt.Fprintf(c.OutOrStdout(), "%s%s\n", prefix, domain.Display(i.Path))
	}
	for _, p := range r.Problems {
		fmt.Fprintf(c.ErrOrStderr(), "%s: %s\n", p.Source, p.Message)
	}
	if r.Truncated {
		fmt.Fprintln(c.ErrOrStderr(), "Results truncated at configured limit")
	}
	return nil
}
func historyCommand(o *options) *cobra.Command {
	h := &cobra.Command{Use: "history", Short: "Inspect, pin or rerun captured searches"}
	var j bool
	h.PersistentFlags().BoolVar(&j, "json", false, "Emit JSON")
	h.AddCommand(&cobra.Command{Use: "list", Short: "List saved searches (no network)", Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error {
		cfg, e := config.Load(o.configPath)
		if e != nil {
			return usage(e)
		}
		rs, e := history.New(cfg).List(c.Context(), 0)
		if e != nil {
			return e
		}
		if j {
			return writeJSON(c, rs)
		}
		for _, r := range rs {
			p := " "
			if r.Pinned {
				p = "*"
			}
			fmt.Fprintf(c.OutOrStdout(), "%s %s  %s  %-9s %s %s\n", p, r.ID, r.StartedAt.Format("2006-01-02 15:04"), r.Status, r.Query.Target.ID(), domain.Display(r.Query.Raw))
		}
		return nil
	}})
	for _, name := range []string{"show", "rerun", "pin", "unpin", "delete"} {
		name := name
		h.AddCommand(&cobra.Command{Use: name + " ID", Short: name + " a saved search", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
			cfg, e := config.Load(o.configPath)
			if e != nil {
				return usage(e)
			}
			s := history.New(cfg)
			if name == "pin" || name == "unpin" {
				e = s.Pin(c.Context(), a[0], name == "pin")
			} else if name == "delete" {
				e = s.Delete(c.Context(), a[0])
			} else {
				r, e := s.Get(c.Context(), a[0])
				if e != nil {
					return e
				}
				if name == "rerun" {
					parent := r.ID
					r = search.New(cfg).Execute(c.Context(), r.Query, nil)
					r.ParentID = parent
					if e := saveRun(c.Context(), cfg, r); e != nil {
						fmt.Fprintln(c.ErrOrStderr(), "History not saved:", e)
					}
					if e := writeRun(c, r, j); e != nil {
						return e
					}
					return runError(r)
				}
				if !j {
					fmt.Fprintf(c.ErrOrStderr(), "Historical snapshot · %s · %s\n", r.CapturedAt.Format(time.RFC3339), r.Status)
				}
				return writeRun(c, r, j)
			}
			if e != nil {
				return e
			}
			if j {
				return writeJSON(c, map[string]any{"id": a[0], "action": name, "ok": true})
			}
			fmt.Fprintln(c.OutOrStdout(), name, a[0])
			return nil
		}})
	}
	return h
}
func configCommand(o *options) *cobra.Command {
	cc := &cobra.Command{Use: "config", Short: "Inspect and edit XDG configuration"}
	cc.AddCommand(&cobra.Command{Use: "path", Short: "Print selected configuration path", Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error {
		p, e := config.ResolvePaths(o.configPath)
		if e != nil {
			return usage(e)
		}
		fmt.Fprintln(c.OutOrStdout(), p.Config)
		return nil
	}})
	cc.AddCommand(&cobra.Command{Use: "show", Short: "Show effective configuration as JSON", Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error {
		cfg, e := config.Load(o.configPath)
		if e != nil {
			return usage(e)
		}
		return writeJSON(c, cfg)
	}})
	cc.AddCommand(&cobra.Command{Use: "init", Short: "Create a default config without overwriting an existing file", Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error {
		cfg := config.Defaults()
		p, e := config.ResolvePaths(o.configPath)
		if e != nil {
			return usage(e)
		}
		cfg.Paths = p
		if e := config.Init(cfg); e != nil {
			return e
		}
		fmt.Fprintln(c.OutOrStdout(), p.Config)
		return nil
	}})
	cc.AddCommand(&cobra.Command{Use: "edit", Short: "Edit the selected configuration and validate it", Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error {
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
			return usage(errors.New("config edit requires a terminal"))
		}
		p, e := config.ResolvePaths(o.configPath)
		if e != nil {
			return e
		}
		cfg := config.Defaults()
		cfg.Paths = p
		if _, e := os.Stat(p.Config); errors.Is(e, os.ErrNotExist) {
			if e := config.Init(cfg); e != nil {
				return e
			}
		}
		item := domain.Item{Path: p.Config, Name: filepath.Base(p.Config), Kind: "file"}
		cmd, e := actions.New(cfg).Prepare(c.Context(), item, "", "editor")
		if e != nil {
			return e
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = c.ErrOrStderr()
		cmd.Stderr = c.ErrOrStderr()
		if e := cmd.Run(); e != nil {
			return e
		}
		_, e = config.Load(p.Config)
		return usage(e)
	}})
	return cc
}
func cacheCommand(o *options) *cobra.Command {
	cc := &cobra.Command{Use: "cache", Short: "Manage disposable previews; history is retained"}
	for _, name := range []string{"stats", "clear"} {
		name := name
		cc.AddCommand(&cobra.Command{Use: name, Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error {
			cfg, e := config.Load(o.configPath)
			if e != nil {
				return usage(e)
			}
			s := cache.New(cfg)
			if name == "clear" {
				if e := s.Clear(); e != nil {
					return e
				}
				fmt.Fprintln(c.OutOrStdout(), "Preview cache cleared; history retained")
				return nil
			}
			stats, e := s.Stats()
			if e != nil {
				return e
			}
			return writeJSON(c, stats)
		}})
	}
	return cc
}
func doctorCommand(o *options) *cobra.Command {
	var j bool
	c := &cobra.Command{Use: "doctor", Short: "Inspect local tools, configuration and host inventory (no SSH connections)", Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error {
		cfg, e := config.Load(o.configPath)
		if e != nil {
			return usage(e)
		}
		tools := map[string]string{}
		for name, path := range map[string]string{"fd": cfg.Tools.FD, "rg": cfg.Tools.RG, "rga": cfg.Tools.RGA, "zoxide": cfg.Tools.Zoxide, "ssh": cfg.Tools.SSH} {
			p, e := exec.LookPath(path)
			if e != nil && name == "fd" && path == "fd" {
				p, e = exec.LookPath("fdfind")
			}
			if e != nil {
				p = "unavailable"
			}
			tools[name] = p
		}
		ctx, cancel := context.WithTimeout(c.Context(), 3*time.Second)
		defer cancel()
		hs, problems := hosts.Discover(ctx, cfg)
		out := map[string]any{"schema_version": 1, "tools": tools, "paths": cfg.Paths, "hosts": hs, "problems": problems}
		if j {
			return writeJSON(c, out)
		}
		for _, name := range []string{"fd", "rg", "rga", "zoxide", "ssh"} {
			fmt.Fprintf(c.OutOrStdout(), "%-8s %s\n", name, tools[name])
		}
		fmt.Fprintf(c.OutOrStdout(), "Config   %s\nHistory  %s\n", cfg.Paths.Config, filepath.Join(cfg.Paths.State, "history.db"))
		for _, p := range problems {
			fmt.Fprintln(c.ErrOrStderr(), p)
		}
		return nil
	}}
	c.Flags().BoolVar(&j, "json", false, "Emit JSON")
	return c
}
