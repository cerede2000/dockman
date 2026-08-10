package auth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/rs/zerolog/log"
)

const CookieHeaderAuth = "Authorization"
const CookieHeaderSessionId = "SessionId"
const KeyUserCtx = "user"

func Middleware(service *Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, ok := CheckAuth(w, r, service)
		if !ok {
			return
		}
		next.ServeHTTP(w, r)
	})
}

const oidcPage = "/api/auth/login/oidc"

// CheckAuth verifies the request's auth cookie. On success it returns a request
// whose context carries the authenticated user (under KeyUserCtx) together with
// ok=true; callers must forward the returned request downstream. On failure it
// writes the appropriate response and returns ok=false.
func CheckAuth(w http.ResponseWriter, r *http.Request, srv *Service) (*http.Request, bool) {
	u, err := verifyCookie(r.Cookies(), srv)
	if err == nil {
		// http.Request.WithContext returns a shallow copy; we must return it so
		// the enriched context actually reaches the downstream handler.
		r = r.WithContext(context.WithValue(
			r.Context(),
			KeyUserCtx, u,
		))
		return r, true
	}

	if srv.config.OIDCEnable && srv.config.OIDCAutoRedirect {
		// IMPORTANT BEFORE CHANGING THE STATUS CODE HERE
		// update the code here as well: ui/src/lib/api.ts:82
		w.WriteHeader(http.StatusFound)
		_, err = w.Write([]byte(oidcPage))
		if err != nil {
			log.Warn().Err(err).Msg("Failed to write response")
		}
		return r, false
	}

	// A fixed message: the reason a cookie failed to verify is Dockman's
	// business, not that of a caller who has not authenticated.
	log.Debug().Err(err).Msg("rejected an unauthenticated request")
	http.Error(w, "authentication required", http.StatusUnauthorized)
	return r, false
}

func getCookie(cookieName string, cookies []*http.Cookie) (*http.Cookie, error) {
	if cookieName == "" {
		return nil, http.ErrNoCookie
	}

	for _, c := range cookies {
		if c.Name == cookieName {
			return c, nil
		}
	}

	return nil, http.ErrNoCookie
}

func verifyCookie(cookies []*http.Cookie, srv *Service) (*User, error) {
	cookie, err := getCookie(CookieHeaderAuth, cookies)
	if err != nil {
		return nil, err
	}

	token := cookie.Value
	userInfo, err := srv.VerifyToken(token)
	if err != nil {
		// No log here, and the reason is propagated rather than flattened.
		// Logging at Error made an expired cookie or an unauthenticated scan -
		// neither of which is a malfunction - fill the log at the level
		// reserved for things that are, and it did so one frame above the
		// caller that deliberately reports this at Debug. Flattening the error
		// on top of that left that Debug line with nothing to say.
		return nil, fmt.Errorf("unable to verify token: %w", err)
	}

	return userInfo, nil
}
