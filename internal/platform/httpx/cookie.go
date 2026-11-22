package httpx

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// maxCookieSize is the size browsers are required to accept (RFC 6265 §6.1).
const maxCookieSize = 4096

// Cookie describes a cookie to set. Secure is always on; the remaining
// attributes default to the hardened choice.
type Cookie struct {
	Name string
	// Value is secret by default (session and flow cookies carry tokens).
	Value secret.String
	// Path defaults to "/".
	Path string
	// Domain is empty by default (host-only cookie).
	Domain string
	// MaxAge 0 makes a session cookie; a negative value deletes the cookie.
	MaxAge time.Duration
	// SameSite defaults to Lax. None is allowed because Secure is enforced;
	// http.SameSiteDefaultMode is rejected as ambiguous.
	SameSite http.SameSite
	// JSReadable leaves HttpOnly off. Use it only for a double-submit CSRF
	// token that scripts must read.
	JSReadable bool
}

// SetCookie validates c and adds a Set-Cookie header. It enforces Secure,
// HttpOnly (unless JSReadable), SameSite=Lax by default and Path=/ by
// default, and the prefix rules: a "__Host-" cookie must have Path=/ and no
// Domain; "__Secure-" is satisfied by Secure. Invalid names, values, paths
// or domains, and cookies larger than 4096 bytes, are errors.
func SetCookie(w http.ResponseWriter, c Cookie) error {
	hc, err := buildCookie(c)
	if err != nil {
		return err
	}
	http.SetCookie(w, hc)
	return nil
}

func buildCookie(c Cookie) (*http.Cookie, error) {
	if c.Path == "" {
		c.Path = "/"
	}
	invalid := func(msg string) error {
		return errs.New(errs.CodeInternal, fmt.Sprintf("cookie %q: %s", c.Name, msg))
	}
	if strings.HasPrefix(strings.ToLower(c.Name), "__host-") {
		if c.Path != "/" {
			return nil, invalid("__Host- cookies need Path=/")
		}
		if c.Domain != "" {
			return nil, invalid("__Host- cookies must not set Domain")
		}
	}
	sameSite := c.SameSite
	switch sameSite {
	case 0:
		sameSite = http.SameSiteLaxMode
	case http.SameSiteLaxMode, http.SameSiteStrictMode, http.SameSiteNoneMode:
	case http.SameSiteDefaultMode:
		return nil, invalid("SameSite must be Lax, Strict or None")
	default:
		return nil, invalid("unknown SameSite mode")
	}
	value := c.Value.Reveal()
	if len(c.Name)+len(value) > maxCookieSize {
		return nil, invalid("name and value exceed 4096 bytes")
	}
	hc := &http.Cookie{ //nolint:gosec // Secure is always set; HttpOnly is off only for an explicit JSReadable cookie
		Name:     c.Name,
		Value:    value,
		Path:     c.Path,
		Domain:   c.Domain,
		Secure:   true,
		HttpOnly: !c.JSReadable,
		SameSite: sameSite,
	}
	switch {
	case c.MaxAge < 0:
		hc.MaxAge = -1
	case c.MaxAge > 0:
		hc.MaxAge = max(1, int(c.MaxAge/time.Second))
	}
	if err := hc.Valid(); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, fmt.Sprintf("cookie %q is invalid", c.Name))
	}
	return hc, nil
}
