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
	"os"
	"strconv"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
)

const (
	// SessionName is the cookie name used for user sessions.
	SessionName = "repo-agent-session"

	// DefaultSessionCookieMaxAge is 30 days in seconds, matching Gorilla sessions default.
	DefaultSessionCookieMaxAge = 86400 * 30
)

// SessionCookieOptions returns the session cookie configuration options.
// The Secure flag defaults to true, but can be disabled for HTTP/local development
// by setting SESSION_COOKIE_SECURE (or COOKIE_SECURE) to "false".
func SessionCookieOptions() sessions.Options {
	secure := true
	if v := os.Getenv("SESSION_COOKIE_SECURE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			secure = b
		}
	} else if v := os.Getenv("COOKIE_SECURE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			secure = b
		}
	}

	return sessions.Options{
		Path:     "/",
		MaxAge:   DefaultSessionCookieMaxAge,
		HttpOnly: true,
		// Secure should only be disabled for local HTTP development.
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// NewSessionStore creates a cookie session store with session cookie options configured.
func NewSessionStore(secret string) cookie.Store {
	store := cookie.NewStore([]byte(secret))
	store.Options(SessionCookieOptions())
	return store
}
