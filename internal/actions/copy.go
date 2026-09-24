package actions

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/daviddwlee84/lazyfind/internal/domain"
)

// CopyFormat is an offline representation of one clipboard choice. Value and
// Base retain raw path bytes; the label is safe to display in a terminal.
type CopyFormat struct {
	ID        string
	Label     string
	Value     string
	Available bool
	Reason    string
	Base      string
}

// Formats computes copy choices using only the saved item and resolved roots.
// It never resolves paths against cwd, stats files or contacts a remote host.
// currentMatchIndex identifies the selected match, including extracted matches
// whose line numbers must never be presented as native source locations.
func Formats(item domain.Item, queryRoots []string, currentMatchIndex int) []CopyFormat {
	raw := item.RawPath()
	_, _, absolute := relativePath(raw, nil, item.Target.Remote())
	absReason := ""
	if !absolute {
		absReason = "Item has no resolved absolute path"
	}
	rel, base, _ := relativePath(raw, queryRoots, item.Target.Remote())
	relReason := ""
	if base == "" {
		relReason = "No resolved search root contains this path"
	}
	line := 0
	lineReason := "No native text match is selected"
	if currentMatchIndex >= 0 && currentMatchIndex < len(item.Matches) {
		m := item.Matches[currentMatchIndex]
		if m.Extracted {
			lineReason = "Extracted document positions are not source-file line numbers"
		} else if m.Line > 0 {
			line = m.Line
			lineReason = ""
		}
	}
	hostLabel := ""
	if item.Target.Remote() {
		hostLabel = " (on " + domain.Display(item.Target.Host) + ")"
	}
	relLabel := "Relative path"
	if base != "" {
		relLabel += " (from " + domain.Display(base) + ")"
	}
	makeFormat := func(id, label, value, root, reason string) CopyFormat {
		return CopyFormat{ID: id, Label: label, Value: value, Available: reason == "", Reason: reason, Base: root}
	}
	withLine := func(value string) string {
		if line > 0 {
			return value + ":" + strconv.Itoa(line)
		}
		return value
	}
	lineUnavailable := func(pathReason string) string {
		if pathReason != "" {
			return pathReason
		}
		return lineReason
	}
	formats := []CopyFormat{
		makeFormat("absolute", "Absolute path"+hostLabel, raw, "", absReason),
		makeFormat("relative", relLabel+hostLabel, rel, base, relReason),
		makeFormat("absolute_line", "Absolute path:line"+hostLabel, withLine(raw), "", lineUnavailable(absReason)),
		makeFormat("relative_line", "Relative path:line"+hostLabel, withLine(rel), base, lineUnavailable(relReason)),
		makeFormat("absolute_reference", "@absolute reference"+hostLabel, "@"+withLine(raw), "", absReason),
		makeFormat("relative_reference", "@relative reference"+hostLabel, "@"+withLine(rel), base, relReason),
	}
	if item.Target.Remote() {
		remote := item.Target.Host + ":" + raw
		formats = append(formats,
			makeFormat("remote", "Remote host:absolute path", remote, "", absReason),
			makeFormat("remote_line", "Remote host:absolute path:line", withLine(remote), "", lineUnavailable(absReason)),
		)
	}
	return formats
}

// BuildCopy returns a raw clipboard value and rejects an unavailable or unknown
// format. Clipboard transport stays with the caller.
func BuildCopy(item domain.Item, queryRoots []string, currentMatchIndex int, formatID string) (string, error) {
	for _, format := range Formats(item, queryRoots, currentMatchIndex) {
		if format.ID == formatID {
			if !format.Available {
				return "", fmt.Errorf("cannot copy %s: %s", format.Label, format.Reason)
			}
			return format.Value, nil
		}
	}
	return "", fmt.Errorf("unknown copy format %q", formatID)
}

// relativePath chooses the deepest containing absolute root. Remote paths use
// POSIX rules even when lazyfind itself is running on a different platform.
func relativePath(raw string, roots []string, remote bool) (relative, base string, absolute bool) {
	clean, isAbs := filepath.Clean, filepath.IsAbs
	separator := string(filepath.Separator)
	if remote {
		clean, isAbs = path.Clean, path.IsAbs
		separator = "/"
	}
	if !isAbs(raw) {
		return "", "", false
	}
	full := clean(raw)
	for _, candidate := range roots {
		if !isAbs(candidate) {
			continue
		}
		root := clean(candidate)
		prefix := strings.TrimSuffix(root, separator) + separator
		if full != root && !strings.HasPrefix(full, prefix) {
			continue
		}
		if len(root) <= len(base) {
			continue
		}
		base = root
		if full == root {
			relative = "."
		} else {
			relative = strings.TrimPrefix(full, prefix)
		}
	}
	return relative, base, true
}
