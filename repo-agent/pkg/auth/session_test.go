/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

func TestSessionCookieOptions_Default(t *testing.T) {
	t.Setenv("SESSION_COOKIE_SECURE", "")
	t.Setenv("COOKIE_SECURE", "")

	opts := SessionCookieOptions()
	if !opts.Secure {
		t.Errorf("expected Secure=true by default, got %v", opts.Secure)
	}
	if !opts.HttpOnly {
		t.Errorf("expected HttpOnly=true, got %v", opts.HttpOnly)
	}
	if opts.Path != "/" {
		t.Errorf("expected Path='/', got %q", opts.Path)
	}
	if opts.MaxAge != DefaultSessionCookieMaxAge {
		t.Errorf("expected MaxAge=%d, got %d", DefaultSessionCookieMaxAge, opts.MaxAge)
	}
	if opts.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=SameSiteLaxMode (%d), got %d", http.SameSiteLaxMode, opts.SameSite)
	}
}

func TestSessionCookieOptions_ExplicitFalse(t *testing.T) {
	tests := []struct {
		envName string
		val     string
	}{
		{"SESSION_COOKIE_SECURE", "false"},
		{"SESSION_COOKIE_SECURE", "0"},
		{"SESSION_COOKIE_SECURE", "FALSE"},
		{"COOKIE_SECURE", "false"},
		{"COOKIE_SECURE", "0"},
	}

	for _, tt := range tests {
		t.Run(tt.envName+"="+tt.val, func(t *testing.T) {
			t.Setenv("SESSION_COOKIE_SECURE", "")
			t.Setenv("COOKIE_SECURE", "")
			t.Setenv(tt.envName, tt.val)

			opts := SessionCookieOptions()
			if opts.Secure {
				t.Errorf("expected Secure=false when %s=%s, got %v", tt.envName, tt.val, opts.Secure)
			}
			if opts.SameSite != http.SameSiteLaxMode {
				t.Errorf("expected SameSite=SameSiteLaxMode, got %d", opts.SameSite)
			}
		})
	}
}

func TestSessionCookieOptions_ExplicitTrue(t *testing.T) {
	tests := []struct {
		envName string
		val     string
	}{
		{"SESSION_COOKIE_SECURE", "true"},
		{"SESSION_COOKIE_SECURE", "1"},
		{"SESSION_COOKIE_SECURE", "TRUE"},
		{"COOKIE_SECURE", "true"},
		{"COOKIE_SECURE", "1"},
	}

	for _, tt := range tests {
		t.Run(tt.envName+"="+tt.val, func(t *testing.T) {
			t.Setenv("SESSION_COOKIE_SECURE", "")
			t.Setenv("COOKIE_SECURE", "")
			t.Setenv(tt.envName, tt.val)

			opts := SessionCookieOptions()
			if !opts.Secure {
				t.Errorf("expected Secure=true when %s=%s, got %v", tt.envName, tt.val, opts.Secure)
			}
		})
	}
}

func TestSessionCookieOptions_Precedence(t *testing.T) {
	t.Setenv("SESSION_COOKIE_SECURE", "false")
	t.Setenv("COOKIE_SECURE", "true")

	opts := SessionCookieOptions()
	if opts.Secure {
		t.Errorf("expected SESSION_COOKIE_SECURE to take precedence over COOKIE_SECURE, got Secure=true")
	}

	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("COOKIE_SECURE", "false")

	opts = SessionCookieOptions()
	if !opts.Secure {
		t.Errorf("expected SESSION_COOKIE_SECURE to take precedence over COOKIE_SECURE, got Secure=false")
	}
}

func TestNewSessionStore_CookieHeaderFlags(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		cookieSecure string
		wantSecure   bool
	}{
		{
			name:         "Secure Enabled (Default)",
			cookieSecure: "",
			wantSecure:   true,
		},
		{
			name:         "Secure Disabled for HTTP",
			cookieSecure: "false",
			wantSecure:   false,
		},
		{
			name:         "Secure Enabled Explicitly",
			cookieSecure: "true",
			wantSecure:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SESSION_COOKIE_SECURE", tt.cookieSecure)
			t.Setenv("COOKIE_SECURE", "")

			store := NewSessionStore("test-secret-key-32-bytes-long!")
			r := gin.New()
			r.Use(sessions.Sessions(SessionName, store))
			r.GET("/set-session", func(c *gin.Context) {
				s := sessions.Default(c)
				s.Set(UserKey, "testuser")
				if err := s.Save(); err != nil {
					c.String(http.StatusInternalServerError, err.Error())
					return
				}
				c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(http.MethodGet, "/set-session", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("handler returned status %d: %s", w.Code, w.Body.String())
			}

			setCookie := w.Header().Get("Set-Cookie")
			if setCookie == "" {
				t.Fatal("expected Set-Cookie header, got none")
			}

			if !strings.Contains(setCookie, SessionName+"=") {
				t.Errorf("Set-Cookie does not contain session name %q: %s", SessionName, setCookie)
			}
			if !strings.Contains(setCookie, "HttpOnly") {
				t.Errorf("Set-Cookie missing HttpOnly: %s", setCookie)
			}
			if !strings.Contains(setCookie, "SameSite=Lax") {
				t.Errorf("Set-Cookie missing SameSite=Lax: %s", setCookie)
			}

			hasSecure := strings.Contains(setCookie, "Secure")
			if tt.wantSecure && !hasSecure {
				t.Errorf("expected Secure attribute in Set-Cookie header, got: %s", setCookie)
			}
			if !tt.wantSecure && hasSecure {
				t.Errorf("expected NO Secure attribute in Set-Cookie header when disabled, got: %s", setCookie)
			}
		})
	}
}
