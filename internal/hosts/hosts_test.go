package hosts

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazyfind/internal/config"
)

func TestStaticSSHIncludesNoMatchExecution(t *testing.T) {
	dir := t.TempDir()
	included := filepath.Join(dir, "conf.d")
	if err := os.Mkdir(included, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "config")
	write(configPath, "Host alpha *.wild !negated\nInclude conf.d/*.conf\nMatch exec \"touch forbidden\"\nInclude skipped.conf\nHost=omega\n")
	write(filepath.Join(included, "one.conf"), "Host beta gamma\nInclude config\n")
	write(filepath.Join(dir, "skipped.conf"), "Host forbidden\n")
	got, warnings := readSSHConfig(configPath, dir)
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if !reflect.DeepEqual(got, []string{"alpha", "beta", "gamma", "omega"}) {
		t.Fatalf("bad aliases: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("Match command executed")
	}
}
func TestSSHQuotedAndEqualsFields(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{{`Include="path with spaces.conf" other.conf #comment`, []string{"Include", "path with spaces.conf", "other.conf"}}, {`Host "one" two #comment`, []string{"Host", "one", "two"}}, {`Host=three`, []string{"Host", "three"}}, {`Host = four`, []string{"Host", "four"}}} {
		got, err := sshFields(tc.line)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q=%v want %v", tc.line, got, tc.want)
		}
	}
}
func TestInventoryPreservesOnlyExplicitAliases(t *testing.T) {
	dev := []byte(`{"schema_version":1,"kind":"ssh_list","complete":false,"aliases":[{"name":"active","status":"active","selectable":true},{"name":"disabled","status":"active","selectable":false},{"name":"deleted","status":"removed","selectable":true}]}`)
	found, warnings := parseInventory("dev", dev)
	if len(found) != 1 || found[0].Alias != "active" || len(warnings) != 1 {
		t.Fatalf("dev: %v %v", found, warnings)
	}
	fleet := []byte(`{"hosts":[{"name":"Local","local":true},{"name":"Remote","ssh_alias":"prod"},{"name":"Needs SSH Config","hostname":"example","port":2222,"user":"alice","identity_file":"/secret"}]}`)
	found, warnings = parseInventory("fleet", fleet)
	if len(found) != 1 || found[0].Alias != "prod" || len(warnings) != 1 || !strings.Contains(warnings[0], "user, port and identity") {
		t.Fatalf("fleet: %v %v", found, warnings)
	}
}
func TestDiscoverConfigWinsDedupAndNoNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh_config")
	if err := os.WriteFile(path, []byte("Host alpha beta\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(t.TempDir(), "nonexistent-ssh")
	cfg := config.Config{Inventory: config.Inventory{SSHConfig: path}, Tools: config.Tools{SSH: ssh}, Hosts: []config.Host{{Name: "Named Alpha", Alias: "alpha"}}}
	found, warnings := Discover(context.Background(), cfg)
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	if len(found) != 2 {
		t.Fatalf("hosts: %v", found)
	}
	for _, h := range found {
		if h.Alias == "alpha" && (h.Name != "Named Alpha" || h.Source != "config") {
			t.Fatalf("config did not win: %+v", h)
		}
	}
}
