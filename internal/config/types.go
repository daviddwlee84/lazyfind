package config

type Config struct {
	Search    Search            `toml:"search" json:"search"`
	UI        UI                `toml:"ui" json:"ui"`
	History   History           `toml:"history" json:"history"`
	Cache     Cache             `toml:"cache" json:"cache"`
	Tools     Tools             `toml:"tools" json:"tools"`
	Roots     []Root            `toml:"roots" json:"roots"`
	Hosts     []Host            `toml:"hosts" json:"hosts"`
	Inventory Inventory         `toml:"inventory" json:"inventory"`
	Actions   []Action          `toml:"actions" json:"actions"`
	Rules     []Rule            `toml:"rules" json:"rules"`
	Keymap    map[string]string `toml:"keymap" json:"keymap"`
	Paths     Paths             `toml:"-" json:"paths"`
}
type Search struct {
	Sources        []string `toml:"sources" json:"sources"`
	Hidden         bool     `toml:"hidden" json:"hidden"`
	Ignored        bool     `toml:"ignored" json:"ignored"`
	MaxResults     int      `toml:"max_results" json:"max_results"`
	MaxMatches     int      `toml:"max_matches" json:"max_matches"`
	TimeoutSeconds int      `toml:"timeout_seconds" json:"timeout_seconds"`
	DebounceMS     int      `toml:"debounce_ms" json:"debounce_ms"`
}
type UI struct {
	Mouse   bool   `toml:"mouse" json:"mouse"`
	Preview bool   `toml:"preview" json:"preview"`
	Color   string `toml:"color" json:"color"`
}
type History struct {
	Enabled           bool `toml:"enabled" json:"enabled"`
	MaxDays           int  `toml:"max_days" json:"max_days"`
	MaxRuns           int  `toml:"max_runs" json:"max_runs"`
	MaxResults        int  `toml:"max_results" json:"max_results"`
	SnippetsPerItem   int  `toml:"snippets_per_item" json:"snippets_per_item"`
	SnippetBytes      int  `toml:"snippet_bytes" json:"snippet_bytes"`
	SnippetTotalBytes int  `toml:"snippet_total_bytes" json:"snippet_total_bytes"`
}
type Cache struct {
	MaxBytes int64 `toml:"max_bytes" json:"max_bytes"`
}
type Tools struct {
	FD     string `toml:"fd" json:"fd"`
	RG     string `toml:"rg" json:"rg"`
	RGA    string `toml:"rga" json:"rga"`
	Zoxide string `toml:"zoxide" json:"zoxide"`
	SSH    string `toml:"ssh" json:"ssh"`
}
type Root struct {
	Name  string   `toml:"name" json:"name"`
	Host  string   `toml:"host" json:"host"`
	Paths []string `toml:"paths" json:"paths"`
}
type Host struct {
	Name  string `toml:"name" json:"name"`
	Alias string `toml:"alias" json:"alias"`
}
type Inventory struct {
	Dev       bool   `toml:"dev" json:"dev"`
	Fleet     bool   `toml:"fleet" json:"fleet"`
	SSHConfig string `toml:"ssh_config" json:"ssh_config"`
}
type Action struct {
	ID       string   `toml:"id" json:"id"`
	Label    string   `toml:"label" json:"label"`
	Argv     []string `toml:"argv" json:"argv"`
	Cwd      string   `toml:"cwd" json:"cwd"`
	Mode     string   `toml:"mode" json:"mode"`
	Location string   `toml:"location" json:"location"`
	Key      string   `toml:"key" json:"key"`
}
type Rule struct {
	Kinds      []string `toml:"kinds" json:"kinds"`
	Extensions []string `toml:"extensions" json:"extensions"`
	MIME       string   `toml:"mime" json:"mime"`
	Git        bool     `toml:"git" json:"git"`
	Location   string   `toml:"location" json:"location"`
	Actions    []string `toml:"actions" json:"actions"`
	Default    string   `toml:"default" json:"default"`
	Preview    string   `toml:"preview" json:"preview"`
}
type Paths struct {
	Config string `json:"config"`
	State  string `json:"state"`
	Cache  string `json:"cache"`
	Data   string `json:"data"`
}
