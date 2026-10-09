package argp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestGetString(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"cacao"}
	want := "default"
	if got := Get("key", "default"); got != want {
		t.Fatalf(`GetString("key", "default") = %v, want %v`, got, want)
	}
	os.Args = append(os.Args, "--key=value")
	want = "value"
	if got := Get("key", "default"); got != want {
		t.Fatalf(`GetString("key", "default") = %v, want %v`, got, want)
	}
}

func withArgs(t *testing.T, args ...string) {
	t.Helper()
	saved := os.Args
	os.Args = append([]string{"cacao"}, args...)
	t.Cleanup(func() { os.Args = saved })
}

func loadTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { LoadConfig("") })
	return path
}

func TestGetPrecedence(t *testing.T) {
	loadTempConfig(t, "loglevel = \"config-level\"\n")

	withArgs(t, "--loglevel=cli-level")
	if got := Get("loglevel", "default"); got != "cli-level" {
		t.Fatalf("command line must win: got %q", got)
	}

	withArgs(t)
	if got := Get("loglevel", "default"); got != "config-level" {
		t.Fatalf("config must win over default: got %q", got)
	}

	loadTempConfig(t, "loglevel = \"\"\n")
	withArgs(t)
	if got := Get("loglevel", "default"); got != "default" {
		t.Fatalf("empty config value must fall back to default: got %q", got)
	}
}

func TestStorageAndConfigAreCLIOnly(t *testing.T) {
	loadTempConfig(t, "storage = \"/tmp/other\"\nconfig = \"/tmp/other.toml\"\n")
	withArgs(t)
	if got := Get("storage", "."); got != "." {
		t.Fatalf("storage must be exempt from config: got %q", got)
	}
	if got := Config("config"); got != "" {
		t.Fatalf("config path must be exempt from config: got %q", got)
	}
	withArgs(t, "--storage=/srv/cacao")
	if got := Get("storage", "."); got != "/srv/cacao" {
		t.Fatalf("storage must still come from CLI: got %q", got)
	}
}

func parsedConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{}
	if err := toml.Unmarshal(data, &values); err != nil {
		t.Fatalf("generated file does not parse: %v", err)
	}
	return values
}

func TestMaintainConfigCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { LoadConfig("") })
	if err := MaintainConfig(); err != nil {
		t.Fatal(err)
	}
	values := parsedConfig(t, path)
	for _, key := range configKeys {
		got, ok := values[key.name]
		if !ok {
			t.Fatalf("created file misses %q", key.name)
		}
		if got != key.defaultValue {
			t.Fatalf("%q = %v, want %q", key.name, got, key.defaultValue)
		}
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		// Windows synthesizes permissions from the inherited ACL, so the
		// 0600 mode cannot be observed there.
		t.Fatalf("config file mode %v, want 0600", info.Mode().Perm())
	}
	if err := MaintainConfig(); err != nil {
		t.Fatal(err)
	}
	if got := Get("loglevel", "fallback"); got != "info" {
		t.Fatalf("created config not picked up by Get: %q", got)
	}
}

func TestMaintainConfigAppendsBeforeTables(t *testing.T) {
	path := loadTempConfig(t, "# keep this comment\nlisten = \":8080\"\n\n[misc]\nfoo = \"bar\"\n")
	if err := MaintainConfig(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	values := parsedConfig(t, path)
	if values["listen"] != ":8080" {
		t.Fatalf("existing value overwritten: %v", values["listen"])
	}
	if values["loglevel"] != "info" {
		t.Fatalf("missing key not added at top level: %v", values["loglevel"])
	}
	misc, ok := values["misc"].(map[string]any)
	if !ok || misc["foo"] != "bar" {
		t.Fatalf("existing table damaged: %v", values["misc"])
	}
	if !strings.Contains(text, "# keep this comment") {
		t.Fatal("existing comment lost")
	}
	if strings.Index(text, "loglevel") > strings.Index(text, "[misc]") {
		t.Fatal("keys appended after table header would not parse at top level")
	}
	if err := MaintainConfig(); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), "loglevel"); count != 1 {
		t.Fatalf("key duplicated across runs: %d occurrences", count)
	}
}

func TestMaintainConfigFailsClosedOnBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("loglevel = \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(path); err == nil {
		t.Fatal("broken config accepted")
	}
	t.Cleanup(func() { LoadConfig("") })
	if err := MaintainConfig(); err == nil {
		t.Fatal("MaintainConfig must report the parse failure")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "loglevel = \n" {
		t.Fatal("broken config file was modified")
	}
}
