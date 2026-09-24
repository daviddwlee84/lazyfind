// Package hosts discovers target suggestions without connecting to the targets.
package hosts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/transport"
)

type Host struct {
	Name   string `json:"name"`
	Alias  string `json:"alias"`
	Source string `json:"source"`
}

// Discover returns configuration, static SSH aliases and explicitly enabled
// inventory adapters. Match exec, SSH canonicalization and network probes are
// intentionally not part of inventory discovery.
func Discover(ctx context.Context, cfg config.Config) ([]Host, []string) {
	result := []Host{}
	warnings := []string{}
	seen := map[string]bool{}
	add := func(h Host) {
		if !validAlias(h.Alias) || seen[h.Alias] {
			return
		}
		if h.Name == "" {
			h.Name = h.Alias
		}
		seen[h.Alias] = true
		result = append(result, h)
	}
	for _, h := range cfg.Hosts {
		if !validAlias(h.Alias) {
			warnings = append(warnings, "configured host "+h.Name+": invalid SSH alias")
			continue
		}
		add(Host{Name: h.Name, Alias: h.Alias, Source: "config"})
	}
	home, err := os.UserHomeDir()
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	sshConfig := cfg.Inventory.SSHConfig
	if sshConfig == "" && home != "" {
		sshConfig = filepath.Join(home, ".ssh", "config")
	}
	if sshConfig != "" {
		if strings.HasPrefix(sshConfig, "~/") {
			sshConfig = filepath.Join(home, strings.TrimPrefix(sshConfig, "~/"))
		}
		aliases, problems := readSSHConfig(sshConfig, filepath.Join(home, ".ssh"))
		warnings = append(warnings, problems...)
		for _, a := range aliases {
			add(Host{Name: a, Alias: a, Source: "ssh_config"})
		}
	}
	runner := transport.New(cfg)
	for _, adapter := range []struct {
		name    string
		enabled bool
		argv    []string
	}{{"dev", cfg.Inventory.Dev, []string{"dev", "ssh", "list", "--json"}}, {"fleet", cfg.Inventory.Fleet, []string{"fleet", "hosts", "--list-json"}}} {
		if !adapter.enabled {
			continue
		}
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, runErr := runner.Run(probe, domain.Target{}, adapter.argv, "")
		cancel()
		if runErr != nil {
			warnings = append(warnings, adapter.name+" inventory: "+runErr.Error())
			continue
		}
		found, problems := parseInventory(adapter.name, out)
		warnings = append(warnings, problems...)
		for _, h := range found {
			add(h)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name) })
	return result, warnings
}

func validAlias(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, "*?![]%$") && strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func readSSHConfig(initial, base string) ([]string, []string) {
	var aliases, warnings []string
	seen := map[string]bool{}
	files := 0
	var read func(string, int)
	read = func(path string, depth int) {
		if depth > 16 || files >= 128 {
			warnings = append(warnings, "SSH Include limit reached")
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		f, err := os.Open(abs)
		if err != nil {
			if !os.IsNotExist(err) || depth > 0 {
				warnings = append(warnings, "SSH config: "+err.Error())
			}
			return
		}
		defer f.Close()
		files++
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		inMatch := false
		for scanner.Scan() {
			fields, err := sshFields(scanner.Text())
			if err != nil {
				warnings = append(warnings, "SSH config "+abs+": "+err.Error())
				continue
			}
			if len(fields) < 2 {
				continue
			}
			switch strings.ToLower(fields[0]) {
			case "match":
				inMatch = true
			case "host":
				inMatch = false
				for _, a := range fields[1:] {
					if validAlias(a) {
						aliases = append(aliases, a)
					}
				}
			case "include":
				if inMatch {
					continue
				}
				for _, pattern := range fields[1:] {
					if strings.ContainsAny(pattern, "$%") || strings.HasPrefix(pattern, "~") && !strings.HasPrefix(pattern, "~/") {
						warnings = append(warnings, "SSH Include with dynamic expansion skipped")
						continue
					}
					if strings.HasPrefix(pattern, "~/") {
						pattern = filepath.Join(filepath.Dir(base), strings.TrimPrefix(pattern, "~/"))
					} else if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(base, pattern)
					}
					matches, err := filepath.Glob(pattern)
					if err != nil {
						warnings = append(warnings, "invalid SSH Include pattern")
						continue
					}
					for _, match := range matches {
						read(match, depth+1)
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			warnings = append(warnings, "SSH config "+abs+": "+err.Error())
		}
	}
	read(initial, 0)
	return aliases, warnings
}

// OpenSSH allows Keyword=value as well as whitespace, quoted paths, and comments.
func sshFields(line string) ([]string, error) {
	var fields []string
	var b strings.Builder
	quoted, escaped, started := false, false, false
	for _, r := range line {
		if escaped {
			b.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			started = true
			continue
		}
		if r == '#' && !quoted {
			break
		}
		if !quoted && r == '=' && len(fields) == 1 && !started {
			continue
		}
		if !quoted && (unicode.IsSpace(r) || r == '=' && len(fields) == 0) {
			if started {
				fields = append(fields, b.String())
				b.Reset()
				started = false
			}
			continue
		}
		b.WriteRune(r)
		started = true
	}
	if quoted || escaped {
		return nil, fmt.Errorf("unterminated quoted field")
	}
	if started {
		fields = append(fields, b.String())
	}
	return fields, nil
}

func parseInventory(source string, data []byte) ([]Host, []string) {
	var result []Host
	var warnings []string
	if source == "dev" {
		var envelope struct {
			SchemaVersion int    `json:"schema_version"`
			Kind          string `json:"kind"`
			Complete      *bool  `json:"complete"`
			Aliases       []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Selectable bool   `json:"selectable"`
			} `json:"aliases"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, []string{"dev inventory: invalid JSON: " + err.Error()}
		}
		if envelope.SchemaVersion != 1 || envelope.Kind != "ssh_list" {
			return nil, []string{"dev inventory: unsupported JSON schema"}
		}
		if envelope.Complete != nil && !*envelope.Complete {
			warnings = append(warnings, "dev inventory is incomplete")
		}
		for _, a := range envelope.Aliases {
			if a.Selectable && (a.Status == "active" || a.Status == "ok") && validAlias(a.Name) {
				result = append(result, Host{Name: a.Name, Alias: a.Name, Source: "dev"})
			}
		}
		return result, warnings
	}
	var envelope struct {
		Hosts []struct {
			Name  string `json:"name"`
			Local bool   `json:"local"`
			Alias string `json:"ssh_alias"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, []string{"fleet inventory: invalid JSON: " + err.Error()}
	}
	if envelope.Hosts == nil {
		return nil, []string{"fleet inventory: expected a hosts array"}
	}
	for _, h := range envelope.Hosts {
		if h.Local {
			continue
		}
		if !validAlias(h.Alias) {
			warnings = append(warnings, "fleet host "+h.Name+": configure an SSH alias to retain its user, port and identity settings")
			continue
		}
		result = append(result, Host{Name: h.Name, Alias: h.Alias, Source: "fleet"})
	}
	return result, warnings
}

// RecentRoots returns zoxide's existing directory ranking without modifying it.
func RecentRoots(ctx context.Context, cfg config.Config, target domain.Target) ([]string, error) {
	tool := cfg.Tools.Zoxide
	if tool == "" {
		tool = "zoxide"
	}
	if !transport.New(cfg).Available(ctx, target, tool) {
		return nil, nil
	}
	out, err := transport.New(cfg).Run(ctx, target, []string{tool, "query", "--list"}, "")
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := map[string]bool{}
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		p := string(line)
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
			if len(paths) == 100 {
				break
			}
		}
	}
	return paths, nil
}
