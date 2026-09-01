package core

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type AuthTokens struct {
	AccessToken             string  `json:"access_token"`
	RefreshToken            *string `json:"refresh_token,omitempty"`
	IDToken                 *string `json:"id_token,omitempty"`
	TokenType               *string `json:"token_type,omitempty"`
	ExpiresIn               *int64  `json:"expires_in,omitempty"`
	ObtainedAt              *string `json:"obtained_at,omitempty"`
	AccountID               *string `json:"account_id,omitempty"`
	ChatGPTAccountIsFedramp bool    `json:"chatgpt_account_is_fedramp,omitempty"`
	SourcePath              string  `json:"-"`
}

func (t AuthTokens) accountLabel() string { return "account " + t.accountAlias() }

func (t AuthTokens) routingKey() string {
	if t.AccountID != nil && *t.AccountID != "" {
		return *t.AccountID
	}
	if t.SourcePath != "" {
		return t.SourcePath
	}
	return "primary"
}

func (t AuthTokens) accountAlias() string {
	digest := sha256.Sum256([]byte(t.routingKey()))
	return fmt.Sprintf("%x", digest[:6])
}

func loadAuthTokens(path string) (AuthTokens, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logError("failed to read auth file %s: %v", pathAlias(path), err)
		}
		return AuthTokens{}, false
	}
	tokens, err := parseAuthTokens(data)
	if err != nil {
		logError("failed to parse auth file %s: %v", pathAlias(path), err)
		return AuthTokens{}, false
	}
	tokens.SourcePath = path
	return tokens, true
}

func loadAllAuthTokens(config Config) []AuthTokens {
	var result []AuthTokens
	for _, path := range authFilePaths(config) {
		if tokens, ok := loadAuthTokens(path); ok {
			result = append(result, tokens)
		}
	}
	return result
}

func loadAuthToken(config Config) (AuthTokens, bool) {
	return loadAuthTokens(authFilePath(config))
}

func (t AuthTokens) save(config Config) error {
	path := t.SourcePath
	if path == "" {
		path = authFilePath(config)
	}
	return t.saveToPath(path)
}

func (t AuthTokens) savePrimary(config Config) error { return t.saveToPath(authFilePath(config)) }

func (t AuthTokens) saveToPath(path string) error {
	if parent := filepath.Dir(path); parent != "." {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return err
		}
	}
	if existing, err := os.ReadFile(path); err == nil {
		var value map[string]any
		if json.Unmarshal(existing, &value) == nil {
			if nested, ok := value["tokens"].(map[string]any); ok {
				nested["access_token"] = t.AccessToken
				nested["refresh_token"] = stringValue(t.RefreshToken)
				if t.IDToken != nil {
					nested["id_token"] = map[string]any{"raw_jwt": *t.IDToken}
				}
				if t.AccountID != nil {
					nested["account_id"] = *t.AccountID
				}
				if t.ChatGPTAccountIsFedramp {
					nested["chatgpt_account_is_fedramp"] = true
				}
				value["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
				data, marshalErr := json.MarshalIndent(value, "", "  ")
				if marshalErr != nil {
					return marshalErr
				}
				return os.WriteFile(path, append(data, '\n'), 0o600)
			}
		}
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func deleteAuthToken(config Config) error {
	path := authFilePath(config)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (t AuthTokens) isExpired() bool {
	if expiration, ok := tokenExpiration(t.AccessToken); ok {
		return !time.Now().UTC().Add(refreshMargin).Before(expiration)
	}
	if t.ObtainedAt == nil {
		return true
	}
	obtained, err := time.Parse(time.RFC3339, *t.ObtainedAt)
	if err != nil {
		return true
	}
	ttl := int64(3600)
	if t.ExpiresIn != nil {
		ttl = *t.ExpiresIn
	}
	expiresAt := obtained.Add(time.Duration(ttl) * time.Second)
	return !time.Now().UTC().Add(refreshMargin).Before(expiresAt)
}

func pathAlias(path string) string {
	digest := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%x", digest[:6])
}

func decodeJWTPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(data, &claims) != nil {
		return nil
	}
	return claims
}

func stringClaim(value any) (string, bool) {
	result, ok := value.(string)
	return result, ok && result != ""
}

func extractAccountID(token string) *string {
	claims := decodeJWTPayload(token)
	if claims == nil {
		return nil
	}
	for _, key := range []string{"https://api.openai.com/auth", "sub"} {
		if value, ok := stringClaim(claims[key]); ok {
			return &value
		}
	}
	return nil
}

func extractChatGPTAccountID(token string) *string {
	claims := decodeJWTPayload(token)
	if claims == nil {
		return nil
	}
	for _, key := range []string{"chatgpt_account_id", "https://api.openai.com/auth"} {
		if value, ok := stringClaim(claims[key]); ok {
			return &value
		}
		if nested, ok := claims[key].(map[string]any); ok {
			if value, ok := stringClaim(nested["chatgpt_account_id"]); ok {
				return &value
			}
		}
	}
	return extractAccountID(token)
}

func extractChatGPTFedramp(token string) bool {
	claims := decodeJWTPayload(token)
	if claims == nil {
		return false
	}
	if value, ok := claims["chatgpt_account_is_fedramp"].(bool); ok {
		return value
	}
	if nested, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		value, _ := nested["chatgpt_account_is_fedramp"].(bool)
		return value
	}
	return false
}

func tokenExpiration(token string) (time.Time, bool) {
	claims := decodeJWTPayload(token)
	value, ok := claims["exp"].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(value), 0), true
}

func parseAuthTokens(data []byte) (AuthTokens, error) {
	var direct AuthTokens
	if err := json.Unmarshal(data, &direct); err == nil && direct.AccessToken != "" {
		return direct, nil
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return AuthTokens{}, err
	}
	nested, ok := value["tokens"].(map[string]any)
	if !ok {
		return AuthTokens{}, errors.New("missing tokens in auth file")
	}
	accessToken, ok := stringClaim(nested["access_token"])
	if !ok {
		return AuthTokens{}, errors.New("missing access_token in auth file")
	}
	refreshToken := optionalString(nested["refresh_token"])
	idToken := optionalIDToken(nested["id_token"])
	accountID := optionalString(nested["account_id"])
	if accountID == nil {
		accountID = extractChatGPTAccountID(stringValue(idToken))
	}
	if accountID == nil {
		accountID = extractAccountID(accessToken)
	}
	fedramp, _ := nested["chatgpt_account_is_fedramp"].(bool)
	if !fedramp && idToken != nil {
		fedramp = extractChatGPTFedramp(*idToken)
	}
	lastRefresh := optionalString(value["last_refresh"])
	tokenType := "Bearer"
	return AuthTokens{AccessToken: accessToken, RefreshToken: refreshToken, IDToken: idToken, TokenType: &tokenType, ObtainedAt: lastRefresh, AccountID: accountID, ChatGPTAccountIsFedramp: fedramp}, nil
}

func optionalString(value any) *string {
	result, ok := stringClaim(value)
	if !ok {
		return nil
	}
	return &result
}

func optionalIDToken(value any) *string {
	if result := optionalString(value); result != nil {
		return result
	}
	if nested, ok := value.(map[string]any); ok {
		return optionalString(nested["raw_jwt"])
	}
	return nil
}

type PKCEChallenge struct{ Verifier, Challenge string }

func generatePKCE() (PKCEChallenge, error) {
	bytes := make([]byte, 64)
	if _, err := rand.Read(bytes); err != nil {
		return PKCEChallenge{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(bytes)
	digest := sha256.Sum256([]byte(verifier))
	return PKCEChallenge{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(digest[:])}, nil
}

func randomState() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func openBrowser(target string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	case "darwin":
		command, args = "open", []string{target}
	default:
		command, args = "xdg-open", []string{target}
	}
	return exec.Command(command, args...).Start()
}

func loginFlow(ctx context.Context) (AuthTokens, error) {
	pkce, err := generatePKCE()
	if err != nil {
		return AuthTokens{}, err
	}
	state, err := randomState()
	if err != nil {
		return AuthTokens{}, err
	}
	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", scopes)
	query.Set("code_challenge", pkce.Challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("state", state)
	query.Set("originator", originator)
	authTarget := authURL + "?" + query.Encode()
	fmt.Println("Opening browser for login…")
	if err := openBrowser(authTarget); err != nil {
		logInfo("could not open browser: %v", err)
		fmt.Printf("Open this URL to continue:\n%s\n", authTarget)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return AuthTokens{}, err
	}
	defer listener.Close()
	logInfo("listening for OAuth callback on http://localhost:1455")
	callback := make(chan string, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			callback <- "error:callback server closed without receiving code"
			return
		}
		defer conn.Close()
		req, readErr := http.ReadRequest(bufio.NewReader(conn))
		result := "error:invalid OAuth callback"
		if readErr == nil && req.URL.Path == "/auth/callback" {
			params := req.URL.Query()
			if oauthError := params.Get("error"); oauthError != "" {
				result = "error:OAuth authorization failed: " + oauthError
			} else if params.Get("state") != state {
				result = "error:OAuth state mismatch"
			} else if params.Get("code") == "" {
				result = "error:OAuth callback did not include a code"
			} else {
				result = "ok:" + params.Get("code")
			}
		}
		body := "<html><body><h2>Login failed</h2><p>Return to the terminal for details.</p></body></html>"
		if strings.HasPrefix(result, "ok:") {
			body = "<html><body><h2>Login successful!</h2><p>You can close this tab.</p><script>window.close()</script></body></html>"
		}
		response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		_, _ = io.WriteString(conn, response)
		callback <- result
	}()
	var result string
	select {
	case result = <-callback:
	case <-time.After(5 * time.Minute):
		return AuthTokens{}, errors.New("OAuth callback timed out after 5 minutes")
	case <-ctx.Done():
		return AuthTokens{}, ctx.Err()
	}
	if !strings.HasPrefix(result, "ok:") {
		return AuthTokens{}, errors.New(strings.TrimPrefix(result, "error:"))
	}
	return exchangeCode(ctx, strings.TrimPrefix(result, "ok:"), pkce.Verifier)
}

func exchangeCode(ctx context.Context, code, verifier string) (AuthTokens, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return AuthTokens{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return AuthTokens{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return AuthTokens{}, fmt.Errorf("token exchange failed (%s): %s", response.Status, strings.TrimSpace(string(body)))
	}
	var raw map[string]any
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		return AuthTokens{}, err
	}
	return parseTokenResponse(raw)
}

func refreshToken(refresh string, config Config) (AuthTokens, error) {
	return refreshTokenWithSource(refresh, "", config)
}

func refreshExistingToken(tokens AuthTokens, config Config) (AuthTokens, error) {
	if tokens.RefreshToken == nil || *tokens.RefreshToken == "" {
		return AuthTokens{}, errors.New("no refresh token available for this account")
	}
	return refreshTokenWithSource(*tokens.RefreshToken, tokens.SourcePath, config)
}

func refreshTokenWithSource(refresh, sourcePath string, config Config) (AuthTokens, error) {
	logInfo("refreshing access token…")
	payload := map[string]string{"grant_type": "refresh_token", "client_id": clientID, "refresh_token": refresh}
	data, _ := json.Marshal(payload)
	request, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(string(data)))
	if err != nil {
		return AuthTokens{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return AuthTokens{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return AuthTokens{}, fmt.Errorf("token refresh failed (%s): %s", response.Status, strings.TrimSpace(string(body)))
	}
	var raw map[string]any
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		return AuthTokens{}, err
	}
	tokens, err := parseTokenResponse(raw)
	if err != nil {
		return AuthTokens{}, err
	}
	if tokens.RefreshToken == nil {
		tokens.RefreshToken = &refresh
	}
	tokens.SourcePath = sourcePath
	if err := tokens.save(config); err != nil {
		return AuthTokens{}, err
	}
	return tokens, nil
}

func revokeToken(token string) error {
	form := url.Values{"client_id": {clientID}, "token": {token}}
	request, err := http.NewRequest(http.MethodPost, revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("token revocation failed (%s): %s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func ensureValidToken(tokens AuthTokens, config Config) (AuthTokens, error) {
	if !tokens.isExpired() {
		return tokens, nil
	}
	if tokens.RefreshToken == nil {
		return AuthTokens{}, errors.New("token expired and no refresh token available. Please run `cproxy login`.")
	}
	return refreshTokenWithSource(*tokens.RefreshToken, tokens.SourcePath, config)
}

func loadAndRefreshAuthCandidates(config Config) ([]AuthTokens, error) {
	tokens := loadAllAuthTokens(config)
	if len(tokens) == 0 {
		return nil, errors.New("not authenticated. Run `cproxy login`.")
	}
	ready := make([]AuthTokens, 0, len(tokens))
	var failures []string
	for _, token := range tokens {
		valid, err := ensureValidToken(token, config)
		if err == nil {
			ready = append(ready, valid)
		} else {
			failures = append(failures, token.accountAlias()+": "+err.Error())
		}
	}
	if len(ready) == 0 {
		return nil, fmt.Errorf("no configured auth accounts are usable: %s", strings.Join(failures, "; "))
	}
	return ready, nil
}

func shouldFallbackStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired || status == http.StatusTooManyRequests
}

type deviceCodeResponse struct {
	UserCode       string          `json:"user_code"`
	LegacyUserCode string          `json:"usercode"`
	DeviceAuthID   string          `json:"device_auth_id"`
	Interval       json.RawMessage `json:"interval"`
}

func (d deviceCodeResponse) pollInterval() time.Duration {
	if len(d.Interval) == 0 {
		return 5 * time.Second
	}
	var number int64
	if json.Unmarshal(d.Interval, &number) == nil && number > 0 {
		return time.Duration(number) * time.Second
	}
	var text string
	if json.Unmarshal(d.Interval, &text) == nil {
		if parsed, err := time.ParseDuration(text + "s"); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 5 * time.Second
}

type deviceCodeSuccessResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

func deviceLoginFlow() (AuthTokens, error) {
	const deviceAuthBase = "https://auth.openai.com/api/accounts"
	const deviceVerifyURL = "https://auth.openai.com/codex/device"
	client := http.DefaultClient
	payload, _ := json.Marshal(map[string]string{"client_id": clientID})
	request, err := http.NewRequest(http.MethodPost, deviceAuthBase+"/deviceauth/usercode", strings.NewReader(string(payload)))
	if err != nil {
		return AuthTokens{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return AuthTokens{}, err
	}
	var device deviceCodeResponse
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return AuthTokens{}, fmt.Errorf("device code request failed (%s): %s", response.Status, strings.TrimSpace(string(body)))
	}
	err = json.NewDecoder(response.Body).Decode(&device)
	response.Body.Close()
	if err != nil {
		return AuthTokens{}, err
	}
	if device.UserCode == "" {
		device.UserCode = device.LegacyUserCode
	}
	fmt.Printf("\nOpen this URL in any browser:\n  %s\n\nEnter this code:\n  %s\n\n(expires in 15 minutes)\n", deviceVerifyURL, device.UserCode)
	interval := device.pollInterval()
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		payload, _ = json.Marshal(map[string]string{"device_auth_id": device.DeviceAuthID, "user_code": device.UserCode})
		request, err = http.NewRequest(http.MethodPost, deviceAuthBase+"/deviceauth/token", strings.NewReader(string(payload)))
		if err != nil {
			return AuthTokens{}, err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err = client.Do(request)
		if err != nil {
			return AuthTokens{}, err
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			var success deviceCodeSuccessResponse
			err = json.NewDecoder(response.Body).Decode(&success)
			response.Body.Close()
			if err != nil {
				return AuthTokens{}, err
			}
			form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {success.AuthorizationCode}, "redirect_uri": {"https://auth.openai.com/deviceauth/callback"}, "code_verifier": {success.CodeVerifier}}
			request, err = http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
			if err != nil {
				return AuthTokens{}, err
			}
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response, err = client.Do(request)
			if err != nil {
				return AuthTokens{}, err
			}
			defer response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				body, _ := io.ReadAll(response.Body)
				return AuthTokens{}, fmt.Errorf("token exchange failed (%s): %s", response.Status, strings.TrimSpace(string(body)))
			}
			var raw map[string]any
			if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
				return AuthTokens{}, err
			}
			return parseTokenResponse(raw)
		}
		status := response.StatusCode
		response.Body.Close()
		if status != http.StatusForbidden && status != http.StatusNotFound {
			return AuthTokens{}, fmt.Errorf("device auth failed (%s)", response.Status)
		}
		sleep := interval
		if remaining := time.Until(deadline); remaining < sleep {
			sleep = remaining
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
	}
	return AuthTokens{}, errors.New("device code timed out after 15 minutes")
}

func parseTokenResponse(raw map[string]any) (AuthTokens, error) {
	access, ok := stringClaim(raw["access_token"])
	if !ok {
		return AuthTokens{}, errors.New("missing access_token in token response")
	}
	refresh := optionalString(raw["refresh_token"])
	idToken := optionalString(raw["id_token"])
	accountID := (*string)(nil)
	if idToken != nil {
		accountID = extractChatGPTAccountID(*idToken)
	}
	if accountID == nil {
		accountID = extractAccountID(access)
	}
	var expires *int64
	if value, ok := raw["expires_in"].(float64); ok {
		n := int64(value)
		expires = &n
	}
	tokenType := optionalString(raw["token_type"])
	obtained := time.Now().UTC().Format(time.RFC3339)
	return AuthTokens{AccessToken: access, RefreshToken: refresh, IDToken: idToken, TokenType: tokenType, ExpiresIn: expires, ObtainedAt: &obtained, AccountID: accountID, ChatGPTAccountIsFedramp: idToken != nil && extractChatGPTFedramp(*idToken)}, nil
}
