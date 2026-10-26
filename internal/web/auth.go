package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const (
	ControlTokenFile     = "control.token"
	ControlCookieName    = "om_control"
	AuthModeAcceptAndLog = "accept-and-log"
	maxWarnedRemotes     = 1024

	// MCPTokenFile holds a second token that opens /mcp and nothing else, so
	// an agent's credential can't reach /api (where the web UI sends).
	MCPTokenFile = "mcp.token"
	// BootstrapCodeFile holds the current single-use web sign-in code, so a
	// headless host can hand its owner a sign-in link without reading logs.
	BootstrapCodeFile = "bootstrap.code"
	AuthModeEnforce   = "enforce"
	controlCookieAge  = 30 * 24 * time.Hour
)

// enforceFromEnv reports whether OPENMESSAGES_REQUIRE_TOKEN asks for
// protected paths to reject requests without a valid token.
func enforceFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OPENMESSAGES_REQUIRE_TOKEN"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

type ControlAuth struct {
	token             string
	mcpToken          string
	enforce           bool
	bootstrap         string
	logger            zerolog.Logger
	dataDir           string
	mu                sync.Mutex
	warned            map[string]struct{}
	warnedMapOverflow bool
}

func NewControlAuth(dataDir string, logger zerolog.Logger) (*ControlAuth, error) {
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	token, regenerated, err := loadOrMintControlToken(absDataDir)
	if err != nil {
		return nil, err
	}
	if regenerated {
		// Regeneration invalidates cookies containing the old token, which is
		// acceptable: a corrupt token cannot authenticate them anyway.
		logger.Warn().Str("path", filepath.Join(absDataDir, ControlTokenFile)).Msg("Invalid control token was regenerated")
	}
	mcpToken, _, err := loadOrMintTokenFile(absDataDir, MCPTokenFile)
	if err != nil {
		return nil, err
	}
	a := &ControlAuth{token: token, mcpToken: mcpToken, enforce: enforceFromEnv(), logger: logger, dataDir: absDataDir, warned: make(map[string]struct{})}
	if err := a.mintBootstrap(); err != nil {
		return nil, err
	}
	return a, nil
}

// mintBootstrap replaces the single-use sign-in code and records it in
// BootstrapCodeFile (0600). Callers hold a.mu or own a not yet shared a.
func (a *ControlAuth) mintBootstrap() error {
	bootstrap, err := randomHex(32)
	if err != nil {
		return fmt.Errorf("mint bootstrap code: %w", err)
	}
	if err := writePrivateFile(filepath.Join(a.dataDir, BootstrapCodeFile), bootstrap); err != nil {
		return fmt.Errorf("record bootstrap code: %w", err)
	}
	a.bootstrap = bootstrap
	return nil
}

func loadOrMintControlToken(dataDir string) (string, bool, error) {
	return loadOrMintTokenFile(dataDir, ControlTokenFile)
}

func loadOrMintTokenFile(dataDir, name string) (string, bool, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", false, fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dataDir, name)
	b, err := os.ReadFile(path)
	if err == nil {
		token := strings.TrimSpace(string(b))
		decoded, decodeErr := hex.DecodeString(token)
		if decodeErr == nil && len(decoded) == 32 {
			if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
				return "", false, fmt.Errorf("secure control token: %w", chmodErr)
			}
			return token, false, nil
		}
		token, mintErr := mintControlToken(path)
		if mintErr != nil {
			return "", false, mintErr
		}
		return token, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("read control token: %w", err)
	}
	token, err := mintControlToken(path)
	return token, false, err
}

func mintControlToken(path string) (token string, err error) {
	token, err = randomHex(32)
	if err != nil {
		return "", fmt.Errorf("mint control token: %w", err)
	}
	if err = writePrivateFile(path, token); err != nil {
		return "", err
	}
	return token, nil
}

// writePrivateFile atomically replaces path with value and a newline, 0600.
func writePrivateFile(path, value string) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary control token: %w", err)
	}
	tempPath := f.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err = f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure temporary control token: %w", err)
	}
	if _, err = f.WriteString(value + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temporary control token: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close temporary control token: %w", err)
	}
	if err = os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install control token: %w", err)
	}
	return nil
}

func randomHex(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *ControlAuth) BootstrapURL(baseURL string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bootstrap == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/auth/bootstrap?t=" + a.bootstrap
}

func (a *ControlAuth) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/bootstrap" {
			if _, ok := localRequestOrigin(r); !ok {
				http.NotFound(w, r)
				return
			}
			a.handleBootstrap(w, r)
			return
		}
		if isProtectedLocalPath(r.URL.Path) {
			classification := a.classify(r)
			fetchSite := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))
			crossSite := fetchSite != "" && fetchSite != "same-origin" && fetchSite != "same-site" && fetchSite != "none"
			if classification == "missing" || classification == "invalid" || crossSite {
				if a.enforce {
					httpError(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				a.warnOnce(r, classification, fetchSite)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *ControlAuth) classify(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth != "" {
		parts := strings.Fields(auth)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if secureEqual(parts[1], a.token) {
				return "bearer-ok"
			}
			if isMCPPath(r.URL.Path) && a.mcpToken != "" && secureEqual(parts[1], a.mcpToken) {
				return "mcp-bearer-ok"
			}
		}
		return "invalid"
	}
	cookie, err := r.Cookie(ControlCookieName)
	if err == nil {
		if secureEqual(cookie.Value, a.token) {
			return "cookie-ok"
		}
		return "invalid"
	}
	return "missing"
}

func isMCPPath(path string) bool {
	return path == "/mcp" || strings.HasPrefix(path, "/mcp/")
}

func secureEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (a *ControlAuth) warnOnce(r *http.Request, classification, fetchSite string) {
	key := r.RemoteAddr
	if host, _, err := net.SplitHostPort(key); err == nil {
		key = host
	}
	if key == "" {
		key = "unknown"
	}
	a.mu.Lock()
	if _, ok := a.warned[key]; ok {
		a.mu.Unlock()
		return
	}
	overflow := false
	if len(a.warned) >= maxWarnedRemotes {
		clear(a.warned)
		if !a.warnedMapOverflow {
			a.warnedMapOverflow = true
			overflow = true
		}
	}
	a.warned[key] = struct{}{}
	a.mu.Unlock()
	if overflow {
		a.logger.Warn().Int("limit", maxWarnedRemotes).Msg("Cleared full unauthenticated remote warning cache")
	}
	a.logger.Warn().Str("remote", key).Str("auth", classification).Str("sec_fetch_site", fetchSite).Msg("Local control request is not authenticated; allowed during accept-and-log rollout")
}

func (a *ControlAuth) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	code := r.URL.Query().Get("t")
	a.mu.Lock()
	valid := a.bootstrap != "" && secureEqual(code, a.bootstrap)
	if valid {
		a.bootstrap = ""
		if err := a.mintBootstrap(); err != nil {
			a.logger.Warn().Err(err).Msg("Could not mint the next bootstrap code")
		}
	}
	a.mu.Unlock()
	if !valid {
		httpError(w, "bootstrap code is invalid or already used", http.StatusGone)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: ControlCookieName, Value: a.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(controlCookieAge.Seconds())})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *ControlAuth) Status() map[string]any {
	if a == nil {
		return map[string]any{"token_present": false, "mode": AuthModeAcceptAndLog}
	}
	mode := AuthModeAcceptAndLog
	if a.enforce {
		mode = AuthModeEnforce
	}
	return map[string]any{"token_present": a.token != "", "mode": mode, "data_dir": a.dataDir}
}
