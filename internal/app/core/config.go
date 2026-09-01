package core

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	clientID              = "app_EMoamEEZ73f0CkXaXp7hrann"
	authURL               = "https://auth.openai.com/oauth/authorize"
	tokenURL              = "https://auth.openai.com/oauth/token"
	revokeURL             = "https://auth.openai.com/oauth/revoke"
	redirectURI           = "http://localhost:1455/auth/callback"
	upstreamBase          = "https://chatgpt.com/backend-api/codex"
	scopes                = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	authFileName          = "auth.json"
	authFileEnv           = "CODEX_AUTH_FILE"
	authFilesEnv          = "CODEX_AUTH_FILES"
	configPathEnv         = "CODEX_OPENAI_PROXY_CONFIG"
	fallbackClientVersion = "0.125.0"
	originator            = "codex_cli_rs"
	refreshMargin         = 5 * time.Minute
	modelsCacheTTL        = 5 * time.Minute
	userDataDirName       = ".compact-proxy"
	configFileName        = "config.toml"
	proxyAPIKeyFileName   = "proxy-api-key"
)

var reasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "persistent"}

// Config is the resolved application configuration. Values are resolved once
// at process startup using this precedence, from lowest to highest priority:
// built-in defaults, TOML file, environment variables, and CLI flags.
//
// Secrets may be supplied through the config file, but PROXY_API_KEY or a
// dedicated secret file is preferred for deployments where the config file is
// readable by more than the service account.
type Config struct {
	ConfigPath             string
	DataDir                string
	Host                   string
	Port                   uint16
	CodexVersion           string
	AuthFile               string
	AuthFiles              []string
	ProxyAPIKey            string
	ProxyAPIKeyFile        string
	DefaultReasoningEffort string
	ConversationMode       string
	UsageModel             string
}

type fileConfig struct {
	Server   fileServerConfig   `toml:"server"`
	Auth     fileAuthConfig     `toml:"auth"`
	Security fileSecurityConfig `toml:"security"`
	Defaults fileDefaultsConfig `toml:"defaults"`
}

type fileServerConfig struct {
	Host         *string `toml:"host"`
	Port         *uint16 `toml:"port"`
	CodexVersion *string `toml:"codex_version"`
}

type fileAuthConfig struct {
	File  *string  `toml:"file"`
	Files []string `toml:"files"`
}

type fileSecurityConfig struct {
	APIKey     *string `toml:"api_key"`
	APIKeyFile *string `toml:"api_key_file"`
}

type fileDefaultsConfig struct {
	ReasoningEffort  *string `toml:"reasoning_effort"`
	ConversationMode *string `toml:"conversation_mode"`
	UsageModel       *string `toml:"usage_model"`
}

// LoadConfig reads the explicit config path, the configured environment path,
// or the user-local default config at ~/.compact-proxy/config.toml. The
// default config is created with built-in values on first use; an explicitly
// requested file must exist and be valid.
func LoadConfig(explicitPath string) (Config, error) {
	config := defaultConfig()
	path := strings.TrimSpace(explicitPath)
	if path == "" {
		path = strings.TrimSpace(os.Getenv(configPathEnv))
	}
	if path == "" {
		path = defaultConfigPath()
		if err := ensureDefaultConfig(path); err != nil {
			return Config{}, err
		}
	}
	path = absolutePath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config file %q: %w", path, err)
	}
	var file fileConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("parse config file %q: %w", path, err)
	}
	applyFileConfig(&config, file)
	config.ConfigPath = path
	if err := applyEnvironment(&config); err != nil {
		return Config{}, err
	}
	if err := validateConfig(&config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func defaultConfig() Config {
	return Config{
		DataDir:          dataDir(),
		Host:             "127.0.0.1",
		Port:             8080,
		ConversationMode: "client",
		UsageModel:       "gpt-5.5",
	}
}

func dataDir() string {
	return absolutePath(filepath.Join(homeDir(), userDataDirName))
}

func defaultConfigPath() string {
	return filepath.Join(dataDir(), configFileName)
}

const defaultConfigContents = `# Default Compact Proxy configuration.
# This file is created automatically in ~/.compact-proxy on first use.

[server]
host = "127.0.0.1"
port = 8080
codex_version = ""

[auth]
# Relative auth paths are resolved from ~/.compact-proxy.
file = "auth.json"
files = []

[security]
# Keep the generated proxy key in ~/.compact-proxy.
api_key_file = "proxy-api-key"

[defaults]
reasoning_effort = ""
conversation_mode = "client"
usage_model = "gpt-5.5"
`

func ensureDefaultConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check default config file %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create user data directory %q: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create default config file %q: %w", path, err)
	}
	if _, err := file.WriteString(defaultConfigContents); err != nil {
		_ = file.Close()
		return fmt.Errorf("write default config file %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close default config file %q: %w", path, err)
	}
	return nil
}

func applyFileConfig(config *Config, file fileConfig) {
	if file.Server.Host != nil {
		config.Host = strings.TrimSpace(*file.Server.Host)
	}
	if file.Server.Port != nil {
		config.Port = *file.Server.Port
	}
	if file.Server.CodexVersion != nil {
		config.CodexVersion = strings.TrimSpace(*file.Server.CodexVersion)
	}
	if file.Auth.File != nil {
		config.AuthFile = strings.TrimSpace(*file.Auth.File)
	}
	if file.Auth.Files != nil {
		config.AuthFiles = append([]string(nil), file.Auth.Files...)
	}
	if file.Security.APIKey != nil {
		config.ProxyAPIKey = strings.TrimSpace(*file.Security.APIKey)
	}
	if file.Security.APIKeyFile != nil {
		config.ProxyAPIKeyFile = strings.TrimSpace(*file.Security.APIKeyFile)
	}
	if file.Defaults.ReasoningEffort != nil {
		config.DefaultReasoningEffort = strings.TrimSpace(*file.Defaults.ReasoningEffort)
	}
	if file.Defaults.ConversationMode != nil {
		config.ConversationMode = strings.TrimSpace(*file.Defaults.ConversationMode)
	}
	if file.Defaults.UsageModel != nil {
		config.UsageModel = strings.TrimSpace(*file.Defaults.UsageModel)
	}
}

func applyEnvironment(config *Config) error {
	if value := strings.TrimSpace(os.Getenv("HOST")); value != "" {
		config.Host = value
	}
	if value := strings.TrimSpace(os.Getenv("PORT")); value != "" {
		port, err := strconv.ParseUint(value, 10, 16)
		if err != nil || port == 0 {
			return fmt.Errorf("invalid PORT %q: expected an integer from 1 to 65535", value)
		}
		config.Port = uint16(port)
	}
	if value := strings.TrimSpace(os.Getenv("CODEX_CLIENT_VERSION")); value != "" {
		config.CodexVersion = value
	}
	if value := strings.TrimSpace(os.Getenv(authFileEnv)); value != "" {
		config.AuthFile = value
	}
	if value := strings.TrimSpace(os.Getenv(authFilesEnv)); value != "" {
		config.AuthFiles = splitConfigList(value)
	}
	if value := strings.TrimSpace(os.Getenv("PROXY_API_KEY")); value != "" {
		config.ProxyAPIKey = value
	}
	if value := strings.TrimSpace(os.Getenv("PROXY_API_KEY_FILE")); value != "" {
		config.ProxyAPIKeyFile = value
	}
	if value := strings.TrimSpace(os.Getenv("DEFAULT_REASONING_EFFORT")); value != "" {
		config.DefaultReasoningEffort = value
	}
	if value := strings.TrimSpace(os.Getenv("CONVERSATION_MODE")); value != "" {
		config.ConversationMode = value
	}
	if value := strings.TrimSpace(os.Getenv("USAGE_MODEL")); value != "" {
		config.UsageModel = value
	}
	return nil
}

func splitConfigList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func validateConfig(config *Config) error {
	config.Host = strings.TrimSpace(config.Host)
	if config.Host == "" {
		return fmt.Errorf("server host cannot be empty")
	}
	if config.Port == 0 {
		return fmt.Errorf("server port must be between 1 and 65535")
	}
	config.CodexVersion = strings.TrimSpace(config.CodexVersion)
	config.DefaultReasoningEffort = strings.ToLower(strings.TrimSpace(config.DefaultReasoningEffort))
	if config.DefaultReasoningEffort != "" && !contains(reasoningEfforts, config.DefaultReasoningEffort) {
		return fmt.Errorf("unsupported default reasoning effort %q", config.DefaultReasoningEffort)
	}
	config.ConversationMode = strings.ToLower(strings.TrimSpace(config.ConversationMode))
	if config.ConversationMode != "client" && config.ConversationMode != "server" {
		return fmt.Errorf("conversation mode must be `client` or `server`, got %q", config.ConversationMode)
	}
	config.UsageModel = strings.TrimSpace(config.UsageModel)
	if config.UsageModel == "" {
		return fmt.Errorf("usage model cannot be empty")
	}
	config.ProxyAPIKey = strings.TrimSpace(config.ProxyAPIKey)
	config.ProxyAPIKeyFile = strings.TrimSpace(config.ProxyAPIKeyFile)
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func absolutePath(path string) string {
	path = expandHome(path)
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" {
		return homeDir()
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}

func homeDir() string {
	if value := os.Getenv("HOME"); value != "" {
		return value
	}
	if value := os.Getenv("USERPROFILE"); value != "" {
		return value
	}
	return "."
}

func resolveDataPath(config Config, raw string, defaultName string) string {
	path := expandHome(raw)
	if path == "" {
		path = defaultName
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	base := strings.TrimSpace(config.DataDir)
	if base == "" {
		base = dataDir()
	} else {
		base = absolutePath(base)
	}
	return filepath.Clean(filepath.Join(base, path))
}

func proxyAPIKeyPath(config Config) string {
	return resolveDataPath(config, config.ProxyAPIKeyFile, proxyAPIKeyFileName)
}

func getProxyAPIKey(config Config) string {
	key := strings.TrimSpace(config.ProxyAPIKey)
	path := proxyAPIKeyPath(config)
	if key == "" {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
			key = strings.TrimSpace(string(data))
		}
	}
	if key == "" {
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err != nil {
			panic("failed to generate proxy API key: " + err.Error())
		}
		key = "sk-codex-proxy-" + hex.EncodeToString(bytes)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
				logWarn("failed to persist generated proxy API key: %v", err)
			}
		} else {
			logWarn("failed to create proxy API key directory: %v", err)
		}
	}
	return key
}

func authFilePath(config Config) string {
	return resolveDataPath(config, config.AuthFile, authFileName)
}

func authFilePaths(config Config) []string {
	paths := make([]string, 0, 2)
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		path := resolveDataPath(config, raw, "")
		for _, existing := range paths {
			if existing == path {
				return
			}
		}
		paths = append(paths, path)
	}
	add(config.AuthFile)
	for _, raw := range config.AuthFiles {
		add(raw)
	}
	if len(paths) == 0 {
		paths = append(paths, resolveDataPath(config, "", authFileName))
	}
	return paths
}

func defaultReasoningEffort(config Config) string {
	return config.DefaultReasoningEffort
}

func terminalUserAgentToken() string {
	if program := strings.TrimSpace(os.Getenv("TERM_PROGRAM")); program != "" {
		if version := strings.TrimSpace(os.Getenv("TERM_PROGRAM_VERSION")); version != "" {
			return sanitizeHeaderToken(program + "/" + version)
		}
		return sanitizeHeaderToken(program)
	}
	if version := strings.TrimSpace(os.Getenv("WEZTERM_VERSION")); version != "" {
		return sanitizeHeaderToken("WezTerm/" + version)
	}
	if term := strings.TrimSpace(os.Getenv("TERM")); term != "" {
		return sanitizeHeaderToken(term)
	}
	return "unknown"
}

func sanitizeHeaderToken(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= ' ' && r <= '~' {
			return r
		}
		return '_'
	}, value)
}

func codexUserAgentForVersion(version string) string {
	return sanitizeHeaderToken(originator + "/" + version + " (" + runtime.GOOS + " " + runtime.Version() + "; " + runtime.GOARCH + ") " + terminalUserAgentToken())
}

type AppState struct {
	HTTP            *http.Client
	Config          Config
	Port            uint16
	ModelsCache     *ModelsCache
	AccountAffinity *AccountAffinity
	Metrics         *Metrics
	clientVersion   atomic.Value
	codexUserAgent  atomic.Value
}

func newAppState(config Config, version string) *AppState {
	userAgent := codexUserAgentForVersion(version)
	state := &AppState{
		HTTP:            &http.Client{},
		Config:          config,
		Port:            config.Port,
		ModelsCache:     newModelsCache(),
		AccountAffinity: new(AccountAffinity),
		Metrics:         new(Metrics),
	}
	state.clientVersion.Store(version)
	state.codexUserAgent.Store(userAgent)
	return state
}

func (s *AppState) ClientVersion() string  { return s.clientVersion.Load().(string) }
func (s *AppState) CodexUserAgent() string { return s.codexUserAgent.Load().(string) }

func fetchLatestCodexVersion() string {
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get("https://registry.npmjs.org/@openai/codex/latest")
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			var payload struct {
				Version string `json:"version"`
			}
			if decodeJSON(response.Body, &payload) == nil && payload.Version != "" {
				return payload.Version
			}
		} else {
			logWarn("npm registry returned status %s", response.Status)
		}
	} else {
		logWarn("failed to fetch Codex version from npm: %v", err)
	}
	return fallbackClientVersion
}

func spawnVersionRefresher(state *AppState) {
	if strings.TrimSpace(state.Config.CodexVersion) != "" {
		return
	}
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			version := fetchLatestCodexVersion()
			state.clientVersion.Store(version)
			state.codexUserAgent.Store(codexUserAgentForVersion(version))
			logInfo("updated Codex client version to %s", version)
		}
	}()
}

func makeState(config Config) *AppState {
	version := strings.TrimSpace(config.CodexVersion)
	if version != "" {
		logInfo("using pinned Codex client version: %s", version)
	} else {
		version = fetchLatestCodexVersion()
		logInfo("using Codex client version: %s", version)
	}
	return newAppState(config, version)
}
