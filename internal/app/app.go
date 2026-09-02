package app

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func Run(args []string) error { return runCLI(args) }

func runCLI(args []string) error {
	args, configPath, err := extractConfigPath(args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("a command is required: serve, login, login-device, logout, auth, or setup")
	}
	config, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if err := setLogLevel(config.LogLevel); err != nil {
		return err
	}
	logInfo("logging configured: level=%s", config.LogLevel)
	switch args[0] {
	case "serve":
		return runServeCommand(args[1:], config)
	case "login":
		tokens, err := loginFlow(context.Background())
		if err != nil {
			return err
		}
		if err := tokens.SavePrimary(config); err != nil {
			return err
		}
		fmt.Println("Logged in successfully.")
		fmt.Println("Account alias:", tokens.AccountAlias())
		return nil
	case "login-device":
		tokens, err := deviceLoginFlow()
		if err != nil {
			return err
		}
		if err := tokens.SavePrimary(config); err != nil {
			return err
		}
		fmt.Println("Logged in successfully via device code.")
		fmt.Println("Account alias:", tokens.AccountAlias())
		return nil
	case "logout":
		if tokens, ok := loadAuthToken(config); ok {
			if err := revokeToken(tokens.AccessToken); err != nil {
				logInfo("token revocation returned an error (may be expected): %v", err)
			}
		}
		if err := deleteAuthToken(config); err != nil {
			return err
		}
		fmt.Println("Logged out successfully.")
		return nil
	case "auth":
		if len(args) != 2 || args[1] != "status" {
			return fmt.Errorf("usage: auth status")
		}
		return runAuthStatus(config)
	case "setup":
		return runSetup(args[1:], config)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func extractConfigPath(args []string) ([]string, string, error) {
	cleaned := make([]string, 0, len(args))
	configPath := ""
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument != "--config" && argument != "-c" && !strings.HasPrefix(argument, "--config=") {
			cleaned = append(cleaned, argument)
			continue
		}
		value := ""
		if strings.HasPrefix(argument, "--config=") {
			value = strings.TrimSpace(strings.TrimPrefix(argument, "--config="))
		} else {
			if index+1 >= len(args) {
				return nil, "", fmt.Errorf("%s requires a path", argument)
			}
			index++
			value = strings.TrimSpace(args[index])
		}
		if value == "" {
			return nil, "", fmt.Errorf("%s requires a non-empty path", argument)
		}
		if configPath != "" {
			return nil, "", fmt.Errorf("config path was provided more than once")
		}
		configPath = value
	}
	return cleaned, configPath, nil
}

func runServeCommand(args []string, config Config) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	port := flags.Uint("port", uint(config.Port), "port to listen on")
	flags.UintVar(port, "p", uint(config.Port), "port to listen on")
	host := flags.String("host", config.Host, "bind address")
	version := flags.String("codex-version", config.CodexVersion, "pin a Codex client version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *port == 0 || *port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	config.Port = uint16(*port)
	config.Host = strings.TrimSpace(*host)
	config.CodexVersion = strings.TrimSpace(*version)
	if config.Host == "" {
		return fmt.Errorf("host cannot be empty")
	}
	return runServer(config)
}

func runServer(config Config) error {
	state := makeState(config)
	spawnVersionRefresher(state)
	logInfo("proxy configuration: host=%s port=%d log_level=%s conversation_mode=%s usage_model=%s", config.Host, config.Port, config.LogLevel, config.ConversationMode, config.UsageModel)
	key := getProxyAPIKey(config)
	if key != "" {
		logInfo("proxy API key auth enabled")
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(requestContextMiddleware(state), corsMiddleware, authMiddleware(key))
	router.GET("/health", healthHandler)
	router.GET("/healthz", healthHandler)
	router.GET("/ready", func(c *gin.Context) { readyHandler(c, state) })
	router.GET("/readyz", func(c *gin.Context) { readyHandler(c, state) })
	router.GET("/metrics", func(c *gin.Context) { metricsHandler(c, state) })
	router.GET("/usage", func(c *gin.Context) { handleUsage(c, state) })
	router.GET("/v1/usage", func(c *gin.Context) { handleUsage(c, state) })
	router.GET("/v1/models", func(c *gin.Context) { handleModels(c, state) })
	router.GET("/v1/capabilities", func(c *gin.Context) { handleCapabilities(c, state) })
	router.GET("/v1/models/:model/capabilities", func(c *gin.Context) { handleModelCapabilities(c, state) })
	router.POST("/v1/responses", func(c *gin.Context) { handleResponses(c, state) })
	router.GET("/v1/responses", func(c *gin.Context) { handleResponsesWebSocket(c, state) })
	router.POST("/v1/responses/compact", func(c *gin.Context) { handleCompact(c, state) })
	router.POST("/v1/chat/completions", func(c *gin.Context) { handleChatCompletions(c, state) })
	router.POST("/v1/images/generations", func(c *gin.Context) { handleImagesGenerations(c, state) })
	router.POST("/v1/images/edits", func(c *gin.Context) { handleImagesEdits(c, state) })
	router.POST("/v1/messages", func(c *gin.Context) { handleMessages(c, state) })
	if parsedHost := strings.TrimSpace(config.Host); parsedHost != "" && parsedHost != "127.0.0.1" && parsedHost != "localhost" && parsedHost != "::1" {
		logWarn("proxy is bound to a public interface (%s); API key authentication is required", config.Host)
	}
	address := fmt.Sprintf("%s:%d", config.Host, config.Port)
	logInfo("cproxy listening on %s", address)
	return router.Run(address)
}

func requestContextMiddleware(state *AppState) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := requestID(c)
		c.Request.Header.Set("x-request-id", id)
		c.Request = c.Request.WithContext(withRequestID(c.Request.Context(), id))
		started := time.Now()
		state.Metrics.RequestsTotal.Add(1)
		c.Header("x-request-id", id)
		logDebug("request started: request_id=%s method=%s endpoint=%s content_length=%d", id, c.Request.Method, c.Request.URL.Path, c.Request.ContentLength)
		c.Next()
		c.Header("x-request-id", id)
		logInfo("request completed: request_id=%s method=%s endpoint=%s status=%d latency_ms=%d", id, c.Request.Method, c.Request.URL.Path, c.Writer.Status(), time.Since(started).Milliseconds())
	}
}

func corsMiddleware(c *gin.Context) {
	setCORSHeaders(c)
	if c.Request.Method == http.MethodOptions {
		c.Status(http.StatusNoContent)
		c.Abort()
		return
	}
	c.Next()
	setCORSHeaders(c)
}

func setCORSHeaders(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "authorization, content-type, openai-beta, anthropic-version, anthropic-beta, x-api-key, x-request-id, x-client-request-id, x-session-id, x-session-affinity")
}

func authMiddleware(expected string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions || c.Request.URL.Path == "/health" || c.Request.URL.Path == "/healthz" || c.Request.URL.Path == "/ready" || c.Request.URL.Path == "/readyz" {
			logDebug("request authentication bypassed: request_id=%s endpoint=%s", c.GetHeader("x-request-id"), c.Request.URL.Path)
			c.Next()
			return
		}
		provided := ""
		if authorization := c.GetHeader("Authorization"); authorization != "" {
			parts := strings.SplitN(authorization, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
				provided = strings.TrimSpace(parts[1])
			}
		}
		if provided == "" {
			provided = c.GetHeader("x-api-key")
		}
		if !constantTimeEqual(provided, expected) {
			logWarn("request authentication rejected: request_id=%s endpoint=%s credential_source=%s", c.GetHeader("x-request-id"), c.Request.URL.Path, credentialSource(c))
			writeJSONError(c, http.StatusUnauthorized, "authentication_error", "missing or invalid proxy API key", "")
			c.Abort()
			return
		}
		logDebug("request authentication accepted: request_id=%s endpoint=%s credential_source=%s", c.GetHeader("x-request-id"), c.Request.URL.Path, credentialSource(c))
		c.Next()
	}
}

func credentialSource(c *gin.Context) string {
	if c.GetHeader("Authorization") != "" {
		return "authorization"
	}
	if c.GetHeader("x-api-key") != "" {
		return "x-api-key"
	}
	return "none"
}

func healthHandler(c *gin.Context) {
	logDebug("health check: request_id=%s", c.GetHeader("x-request-id"))
	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func readyHandler(c *gin.Context, state *AppState) {
	accounts := loadAllAuthTokens(state.Config)
	ready := false
	for _, account := range accounts {
		if !account.IsExpired() || account.RefreshToken != nil {
			ready = true
			break
		}
	}
	status := http.StatusServiceUnavailable
	statusText := "not_ready"
	if ready {
		status = http.StatusOK
		statusText = "ready"
	}
	logDebug("readiness evaluated: request_id=%s ready=%t configured_accounts=%d", c.GetHeader("x-request-id"), ready, len(accounts))
	c.JSON(status, map[string]any{"status": statusText, "auth_ready": ready, "configured_accounts": len(accounts)})
}

func metricsHandler(c *gin.Context, state *AppState) {
	logDebug("metrics requested: request_id=%s", c.GetHeader("x-request-id"))
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(state.Metrics.Render()))
}

func runAuthStatus(config Config) error {
	accounts := loadAllAuthTokens(config)
	if len(accounts) == 0 {
		fmt.Println("Authenticated: no")
		fmt.Println("Run `cproxy login` to authenticate.")
		return nil
	}
	tokens := accounts[0]
	fmt.Println("Authenticated: yes")
	fmt.Println("Account alias:", tokens.AccountAlias())
	expired := tokens.IsExpired()
	fmt.Println("Token expired:", expired)
	if tokens.ObtainedAt != nil {
		fmt.Println("Obtained at:", *tokens.ObtainedAt)
	}
	if expired {
		if tokens.RefreshToken != nil {
			fmt.Println("Refresh token: present (will auto-refresh)")
		} else {
			fmt.Println("Refresh token: none (please re-login)")
		}
	}
	return nil
}

func runSetup(args []string, config Config) error {
	if len(args) == 0 || args[0] != "crush" {
		return fmt.Errorf("usage: setup crush [--base-url URL]")
	}
	flags := flag.NewFlagSet("setup crush", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	baseURL := flags.String("base-url", fmt.Sprintf("http://%s:%d/v1", config.Host, config.Port), "public base URL")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	proxyAPIKey := getProxyAPIKey(config)
	fmt.Println("Add this to ~/.config/crush/crushrc:")
	fmt.Println()
	fmt.Println("provider add codex-proxy \\")
	fmt.Println("  --type openai \\")
	fmt.Printf("  --base-url %q \\\n", *baseURL)
	fmt.Printf("  --api-key %q \\\n", proxyAPIKey)
	fmt.Println("  --discover-models true")

	fmt.Println()
	fmt.Println("Fetching model metadata for an explicit Crush model configuration...")
	models, err := discoverCrushModels(*baseURL, proxyAPIKey)
	if err != nil {
		fmt.Printf("Could not fetch model metadata: %v\n", err)
		fmt.Println("Start the proxy and run `cproxy setup crush` again to include explicit reasoning levels.")
		return nil
	}
	if len(models) == 0 {
		fmt.Println("The proxy returned no models; run setup again after models are available.")
		return nil
	}
	fmt.Println()
	fmt.Println("Add these explicit model definitions to crushrc for scalar metadata:")
	fmt.Println(marshalCrushrcModels(models))
	fmt.Println()
	configJSON, err := marshalCrushConfig(*baseURL, proxyAPIKey, models)
	if err != nil {
		return fmt.Errorf("build Crush model configuration: %w", err)
	}
	fmt.Println("The crushrc commands above cannot carry reasoning_levels with current Crush flags.")
	fmt.Println("Merge this JSON into crush.json when you need the full reasoning-level picker:")
	fmt.Println(string(configJSON))
	return nil
}
