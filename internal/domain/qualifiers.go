package domain

// Qualifier is the shared vocabulary for parsing, completion and help.
type Qualifier struct {
	Name, Description, Example string
	Values                     []string
}

func Qualifiers() []Qualifier {
	return []Qualifier{
		{"type", "File, directory or symlink", "type:dir", []string{"file", "dir", "symlink"}},
		{"ext", "Extensions; comma-separated alternatives", "ext:md,pdf", []string{"md", "txt", "go", "py", "rs", "json", "yaml", "toml", "pdf"}},
		{"mtime", "Modified within a relative duration", "mtime:<7d", []string{"<1d", "<7d", "<30d"}},
		{"after", "Modified on or after date / RFC3339", "after:2026-09-01", nil},
		{"before", "Modified before date / RFC3339", "before:2026-10-01", nil},
		{"size", "File byte size; supports KiB/MiB/GiB", "size:>10MiB", []string{">1MiB", ">10MiB", ">1GiB", "<1MiB"}},
		{"hidden", "Include hidden files and directories", "hidden:true", []string{"true", "false"}},
		{"ignored", "Include Git/fd ignored entries", "ignored:true", []string{"true", "false"}},
		{"depth", "Maximum traversal depth", "depth:3", []string{"1", "2", "3", "5"}},
	}
}
func HasSearchIntent(q QuerySpec) bool {
	f := q.Filters
	return q.Text != "" || len(f.Kinds) > 0 || len(f.Extensions) > 0 || f.Depth > 0 || f.ModifiedWithin != "" || f.After != "" || f.Before != "" || f.MinSize != nil || f.MaxSize != nil
}
