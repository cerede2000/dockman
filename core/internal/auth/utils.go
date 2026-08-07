package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
)

func createAuthCookies(sessionToken string, sessionId uint, expiry time.Time, secure bool) []http.Cookie {
	var cookies []http.Cookie

	return append(
		cookies,
		createCookie(
			CookieHeaderAuth,
			sessionToken,
			expiry,
			secure,
		),
		createCookie(
			CookieHeaderSessionId,
			strconv.Itoa(int(sessionId)),
			expiry,
			secure,
		),
	)
}

// createCookie carries Secure only when the session actually travels over TLS.
//
// The attribute used to be commented out entirely, so a Dockman serving HTTPS
// handed out a session token the browser would happily send back in clear over
// any plain HTTP request to the same host. Setting it unconditionally is not an
// option either: a LAN deployment over plain HTTP - the common homelab case -
// would hand out a cookie the browser never returns, and nobody could log in.
// Hence per-request: TLS here, or a proxy in front telling us it terminated it.
func createCookie(value string, token string, expiresAt time.Time, secure bool) http.Cookie {
	cookie := http.Cookie{
		Name:     value,
		Value:    token,
		Expires:  expiresAt,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	}
	return cookie
}

// requestIsHTTPS reports whether this request reached Dockman over TLS, either
// directly or through a reverse proxy that terminated it. Trusting the header
// errs towards setting Secure: a cookie marked Secure on a plain connection
// breaks login visibly, where a missing one leaks the token silently.
func requestIsHTTPS(tlsServed bool, header http.Header) bool {
	if tlsServed {
		return true
	}
	proto := header.Get("X-Forwarded-Proto")
	if comma := strings.IndexByte(proto, ','); comma >= 0 {
		proto = proto[:comma]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func CreateAuthToken(length int) string {
	const characters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	var randomString []byte

	for i := 0; i < length; i++ {
		// A failing entropy source must never silently degrade into a
		// predictable token; there is nothing sane to continue with.
		randomIndex, err := rand.Int(rand.Reader, big.NewInt(int64(len(characters))))
		if err != nil {
			log.Panic().Err(err).Msg("system entropy source is unavailable; refusing to issue a weak token")
		}
		randomString = append(randomString, characters[randomIndex.Int64()])
	}

	return string(randomString)
}

func checkPassword(inputPassword string, hashedPassword string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(inputPassword))
	if err != nil {
		// Debug, not Error: a wrong password is an expected event, and logging
		// it at error level lets anyone fill the log by guessing.
		log.Debug().Err(err).Msg("password check failed")
		return false
	}
	return true
}

func encryptPassword(password string) (string, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("unable to generate passoword")
	}
	return string(hashedPassword), nil
}

func hashString(input string) string {
	hash := sha256.New()
	hash.Write([]byte(input))
	// Get the resulting hash sum
	hashedBytes := hash.Sum(nil)
	// Convert the hashed bytes to a hexadecimal string
	hashedString := fmt.Sprintf("%x", hashedBytes)
	return hashedString
}
