package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

var testEnvToken = strings.Repeat("ab12", 16)

func enforceHandler(t *testing.T, require bool, envToken string) http.Handler {
	t.Helper()
	t.Setenv(EnvControlToken, envToken)
	if require {
		t.Setenv(EnvRequireToken, "1")
	} else {
		t.Setenv(EnvRequireToken, "")
	}
	a, err := NewControlAuth(t.TempDir(), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return a.Enforce(a.Handler(next))
}

func TestControlTokenEnvValidation(t *testing.T) {
	for _, bad := range []string{"short", strings.Repeat("z", 64), testEnvToken + "00"} {
		t.Setenv(EnvControlToken, bad)
		_, err := NewControlAuth(t.TempDir(), zerolog.Nop())
		if err == nil {
			t.Fatalf("expected error for %q", bad)
		}
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("error echoes the value: %v", err)
		}
	}
	dir := t.TempDir()
	t.Setenv(EnvControlToken, testEnvToken)
	a, err := NewControlAuth(dir, zerolog.Nop())
	if err != nil || a.token != testEnvToken {
		t.Fatalf("env token not used: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ControlTokenFile)); statErr == nil {
		t.Fatal("env token must not be written to disk")
	}
}

func TestEnforceMatrix(t *testing.T) {
	h := enforceHandler(t, true, testEnvToken)
	cookie := &http.Cookie{Name: ControlCookieName, Value: testEnvToken}
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testEnvToken) }
	tests := []struct {
		name  string
		url   string
		setup func(*http.Request)
		want  int
	}{
		{"bearer non-loopback host", "http://openmessage.railway.internal:7007/api/status", bearer, 204},
		{"bearer non-loopback mcp foreign origin", "http://openmessage.railway.internal:7007/mcp/sse", func(r *http.Request) {
			bearer(r)
			r.Header.Set("Origin", "https://evil.example")
		}, 204},
		{"bearer loopback", "http://127.0.0.1:7007/api/status", bearer, 204},
		{"missing non-loopback", "http://openmessage.railway.internal:7007/api/status", func(*http.Request) {}, 401},
		{"missing loopback", "http://127.0.0.1:7007/api/status", func(*http.Request) {}, 401},
		{"wrong bearer", "http://127.0.0.1:7007/api/status", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, 401},
		{"wrong bearer with valid cookie", "http://127.0.0.1:7007/api/status", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer nope")
			r.AddCookie(cookie)
		}, 401},
		{"cookie loopback", "http://127.0.0.1:7007/api/status", func(r *http.Request) { r.AddCookie(cookie) }, 204},
		{"cookie non-loopback host", "http://openmessage.railway.internal:7007/api/status", func(r *http.Request) { r.AddCookie(cookie) }, 403},
		{"cookie loopback foreign origin", "http://127.0.0.1:7007/api/status", func(r *http.Request) {
			r.AddCookie(cookie)
			r.Header.Set("Origin", "https://evil.example")
		}, 403},
		{"wrong cookie", "http://127.0.0.1:7007/api/status", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: ControlCookieName, Value: "bad"})
		}, 401},
		{"bootstrap non-loopback hidden", "http://openmessage.railway.internal:7007/auth/bootstrap?t=x", bearer, 404},
		{"static ui needs nothing", "http://openmessage.railway.internal:7007/", func(*http.Request) {}, 204},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.url, nil)
			tt.setup(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
			if tt.want == 401 && w.Body.Len() != 0 {
				t.Fatalf("401 must have no body, got %q", w.Body.String())
			}
		})
	}
}

func TestEnforceOffIsUnchanged(t *testing.T) {
	h := enforceHandler(t, false, "")
	cases := map[string]int{
		"http://127.0.0.1:7007/api/status":                    204,
		"http://openmessage.railway.internal:7007/api/status": 403,
	}
	for url, want := range cases {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s = %d, want %d", url, w.Code, want)
		}
	}
}
