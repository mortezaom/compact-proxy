package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigAppliesEnvironmentOverFile(t *testing.T) {
	for _, name := range []string{
		"HOST",
		"PORT",
		"LOG_LEVEL",
		"CODEX_CLIENT_VERSION",
		"CODEX_AUTH_FILE",
		"CODEX_AUTH_FILES",
		"PROXY_API_KEY",
		"PROXY_API_KEY_FILE",
		"DEFAULT_REASONING_EFFORT",
		"CONVERSATION_MODE",
		"USAGE_MODEL",
	} {
		unsetEnv(t, name)
	}

	path := filepath.Join(t.TempDir(), "proxy.toml")
	contents := `
[server]
host = "0.0.0.0"
port = 9000
log_level = "warn"
codex_version = "file-version"

[auth]
file = "file-auth.json"
files = ["backup-a.json", "backup-b.json"]

[security]
api_key = "file-key"
api_key_file = "file-key-path"

[defaults]
reasoning_effort = "low"
conversation_mode = "server"
usage_model = "file-model"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("PORT", "9100")
	t.Setenv("LOG_LEVEL", "DEBUG")
	t.Setenv("CODEX_CLIENT_VERSION", "env-version")
	t.Setenv("CODEX_AUTH_FILE", "env-auth.json")
	t.Setenv("CODEX_AUTH_FILES", "env-backup-a.json, env-backup-b.json")
	t.Setenv("PROXY_API_KEY", "env-key")
	t.Setenv("PROXY_API_KEY_FILE", "env-key-path")
	t.Setenv("DEFAULT_REASONING_EFFORT", "HIGH")
	t.Setenv("CONVERSATION_MODE", "CLIENT")
	t.Setenv("USAGE_MODEL", "env-model")

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.ConfigPath != mustAbsolutePath(path) {
		t.Fatalf("ConfigPath = %q, want %q", config.ConfigPath, mustAbsolutePath(path))
	}
	if config.Host != "127.0.0.1" || config.Port != 9100 || config.CodexVersion != "env-version" {
		t.Fatalf("server config = %#v", config)
	}
	if config.LogLevel != "debug" {
		t.Fatalf("LogLevel = %q, want debug", config.LogLevel)
	}
	if config.AuthFile != "env-auth.json" || strings.Join(config.AuthFiles, ",") != "env-backup-a.json,env-backup-b.json" {
		t.Fatalf("auth config = %#v", config)
	}
	if config.ProxyAPIKey != "env-key" || config.ProxyAPIKeyFile != "env-key-path" {
		t.Fatalf("security config = %#v", config)
	}
	if config.DefaultReasoningEffort != "high" || config.ConversationMode != "client" || config.UsageModel != "env-model" {
		t.Fatalf("defaults config = %#v", config)
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.toml")
	if err := os.WriteFile(path, []byte("[server]\nunknown = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("LoadConfig error = %v, want unknown-field error", err)
	}
}

func TestLoadConfigRejectsInvalidEnvironment(t *testing.T) {
	unsetEnv(t, configPathEnv)
	path := filepath.Join(t.TempDir(), "proxy.toml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORT", "not-a-port")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig accepted an invalid PORT")
	}
}

func TestLoadConfigRejectsInvalidLogLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.toml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOG_LEVEL", "trace")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unsupported log level") {
		t.Fatalf("LoadConfig error = %v, want invalid log-level error", err)
	}
}

func TestLoadConfigCreatesDefaultUserConfig(t *testing.T) {
	temporaryHome := t.TempDir()
	t.Setenv("HOME", temporaryHome)
	t.Setenv("USERPROFILE", temporaryHome)
	for _, name := range []string{
		configPathEnv,
		"HOST",
		"PORT",
		"LOG_LEVEL",
		"CODEX_CLIENT_VERSION",
		"CODEX_AUTH_FILE",
		"CODEX_AUTH_FILES",
		"PROXY_API_KEY",
		"PROXY_API_KEY_FILE",
		"DEFAULT_REASONING_EFFORT",
		"CONVERSATION_MODE",
		"USAGE_MODEL",
	} {
		unsetEnv(t, name)
	}

	config, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	wantDataDir := filepath.Join(temporaryHome, userDataDirName)
	wantConfigPath := filepath.Join(wantDataDir, configFileName)
	if config.DataDir != wantDataDir {
		t.Fatalf("DataDir = %q, want %q", config.DataDir, wantDataDir)
	}
	if config.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", config.LogLevel)
	}
	if config.ConfigPath != wantConfigPath {
		t.Fatalf("ConfigPath = %q, want %q", config.ConfigPath, wantConfigPath)
	}
	if _, err := os.Stat(wantConfigPath); err != nil {
		t.Fatalf("default config was not created: %v", err)
	}
	if got := authFilePath(config); got != filepath.Join(wantDataDir, authFileName) {
		t.Fatalf("default auth path = %q, want %q", got, filepath.Join(wantDataDir, authFileName))
	}
	if got := proxyAPIKeyPath(config); got != filepath.Join(wantDataDir, proxyAPIKeyFileName) {
		t.Fatalf("default proxy key path = %q, want %q", got, filepath.Join(wantDataDir, proxyAPIKeyFileName))
	}
	contents, err := os.ReadFile(wantConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "[server]") {
		t.Fatalf("default config contents = %q, want TOML sections", string(contents))
	}
}

func TestGeneratedProxyAPIKeyUsesUserDataDir(t *testing.T) {
	temporaryHome := t.TempDir()
	t.Setenv("HOME", temporaryHome)
	t.Setenv("USERPROFILE", temporaryHome)

	config := defaultConfig()
	key := getProxyAPIKey(config)
	if key == "" {
		t.Fatal("getProxyAPIKey() returned an empty key")
	}
	data, err := os.ReadFile(filepath.Join(dataDir(), proxyAPIKeyFileName))
	if err != nil {
		t.Fatalf("read generated proxy key: %v", err)
	}
	if strings.TrimSpace(string(data)) != key {
		t.Fatalf("persisted proxy key does not match generated key")
	}
}

func TestAuthFilePathsExpandHomeAndDeduplicate(t *testing.T) {
	config := Config{
		AuthFile:  "primary.json",
		AuthFiles: []string{"backup.json", "~/legacy.json", "primary.json", "backup.json"},
	}
	want := []string{
		filepath.Join(dataDir(), "primary.json"),
		filepath.Join(dataDir(), "backup.json"),
		filepath.Join(homeDir(), "legacy.json"),
	}
	paths := authFilePaths(config)
	if len(paths) != len(want) {
		t.Fatalf("authFilePaths() = %#v, want %#v", paths, want)
	}
	for index := range want {
		if paths[index] != want[index] {
			t.Fatalf("authFilePaths()[%d] = %q, want %q", index, paths[index], want[index])
		}
	}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	previous, wasSet := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(name, previous)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func mustAbsolutePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		panic(err)
	}
	return filepath.Clean(absolute)
}
