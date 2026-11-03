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

	"github.com/rs/zerolog"
)

const (
	ControlTokenFile     = "control.token"
	ControlCookieName    = "om_control"
	AuthModeAcceptAndLog = "accept-and-log"
	AuthModeEnforce      = "enforce"

	// EnvControlToken supplies the control token (64 hex chars) instead of
	// <datadir>/control.token. EnvRequireToken=1 enforces it on protected paths.
	EnvControlToken = "OPENMESSAGES_CONTROL_TOKEN"
	EnvRequireToken = "OPENMESSAGES_REQUIRE_TOKEN"
	maxWarnedRemotes     = 1024
)

type ControlAuth struct {
	token             string
	require           bool
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
	var token string
	regenerated := false
	if envToken := strings.TrimSpace(os.Getenv(EnvControlToken)); envToken != "" {
		// The env token overrides the file and is never written to disk.
		if decoded, decodeErr := hex.DecodeString(envToken); decodeErr != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("%s must be 64 hex characters (32 bytes)", EnvControlToken)
		}
		token = envToken
	} else {
		var err error
		token, regenerated, err = loadOrMintControlToken(absDataDir)
		if err != nil {
			return nil, err
		}
	}
	require := os.Getenv(EnvRequireToken) == "1"
	if require && token == "" {
		return nil, fmt.Errorf("%s=1 requires a control token", EnvRequireToken)
	}
	if regenerated {
		// Regeneration invalidates cookies containing the old token, which is
		// acceptable: a corrupt token cannot authenticate them anyway.
		logger.Warn().Str("path", filepath.Join(absDataDir, ControlTokenFile)).Msg("Invalid control token was regenerated")
	}
	bootstrap, err := randomHex(32)
	if err != nil {
		return nil, fmt.Errorf("mint bootstrap code: %w", err)
	}
	return &ControlAuth{token: token, require: require, bootstrap: bootstrap, logger: logger, dataDir: absDataDir, warned: make(map[string]struct{})}, nil
}

func loadOrMintControlToken(dataDir string) (string, bool, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", false, fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dataDir, ControlTokenFile)
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
	f, err := os.CreateTemp(filepath.Dir(path), ".control.token-*")
	if err != nil {
		return "", fmt.Errorf("create temporary control token: %w", err)
	}
	tempPath := f.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err = f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("secure temporary control token: %w", err)
	}
	if _, err = f.WriteString(token + "\n"); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write temporary control token: %w", err)
	}
	if err = f.Close(); err != nil {
		return "", fmt.Errorf("close temporary control token: %w", err)
	}
	if err = os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("install control token: %w", err)
	}
	return token, nil
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

// BootstrapURLFile holds the single-use bootstrap URL under the data dir. The URL
// is a credential, so it is never printed or logged in any mode, terminal or not.
const BootstrapURLFile = "bootstrap.url"

// PublishBootstrapURL writes the single-use bootstrap URL to a new 0600 temp file
// and atomically renames it into place, so a pre-existing file or symlink at the
// destination is replaced rather than followed. It returns the file path, which is
// safe to log. The file is removed once the code is consumed.
func (a *ControlAuth) PublishBootstrapURL(baseURL string) (string, error) {
	url := a.BootstrapURL(baseURL)
	if url == "" {
		return "", nil
	}
	path := filepath.Join(a.dataDir, BootstrapURLFile)
	f, err := os.CreateTemp(a.dataDir, ".bootstrap.url-*")
	if err != nil {
		return "", fmt.Errorf("create temporary bootstrap url file: %w", err)
	}
	tempPath := f.Name()
	defer os.Remove(tempPath)
	if err = f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("secure temporary bootstrap url file: %w", err)
	}
	if _, err = f.WriteString(url + "\n"); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write temporary bootstrap url file: %w", err)
	}
	if err = f.Close(); err != nil {
		return "", fmt.Errorf("close temporary bootstrap url file: %w", err)
	}
	if err = os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("install bootstrap url file: %w", err)
	}
	return path, nil
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
		if !a.require && isProtectedLocalPath(r.URL.Path) {
			classification := a.classify(r)
			fetchSite := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))
			if classification == "missing" || classification == "invalid" || (fetchSite != "" && fetchSite != "same-origin" && fetchSite != "same-site" && fetchSite != "none") {
				a.warnOnce(r, classification, fetchSite)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Enforce wraps next with the loopback guard. When OPENMESSAGES_REQUIRE_TOKEN=1
// it also enforces the control token on protected paths: a valid Bearer token
// is accepted from any Host/Origin; a valid om_control cookie must still pass
// the loopback checks; everything else is 401 with no detail.
func (a *ControlAuth) Enforce(next http.Handler) http.Handler {
	guarded := ProtectLocalControl(next)
	if a == nil || !a.require {
		return guarded
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isProtectedLocalPath(r.URL.Path) {
			guarded.ServeHTTP(w, r)
			return
		}
		switch a.classify(r) {
		case "bearer-ok":
			setSecurityHeaders(w)
			w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
			next.ServeHTTP(w, r)
		case "cookie-ok":
			guarded.ServeHTTP(w, r)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
}

func (a *ControlAuth) classify(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth != "" {
		parts := strings.Fields(auth)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && secureEqual(parts[1], a.token) {
			return "bearer-ok"
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
		_ = os.Remove(filepath.Join(a.dataDir, BootstrapURLFile))
	}
	a.mu.Unlock()
	if !valid {
		httpError(w, "bootstrap code is invalid or already used", http.StatusGone)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: ControlCookieName, Value: a.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *ControlAuth) Status() map[string]any {
	if a == nil {
		return map[string]any{"token_present": false, "mode": AuthModeAcceptAndLog}
	}
	mode := AuthModeAcceptAndLog
	if a.require {
		mode = AuthModeEnforce
	}
	return map[string]any{"token_present": a.token != "", "mode": mode, "data_dir": a.dataDir}
}
