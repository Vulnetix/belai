package agentscan

import (
	"errors"
	"os"

	"github.com/vulnetix/belai/internal/kiroauth"
)

// scanKiro imports the AWS sign-in Kiro's IDE or CLI made: the refresh token
// in the AWS SSO cache and the client registration it names, as the kiro
// provider's login. A GitHub or Google Kiro login refreshes through Kiro's
// own service and is reported, not imported.
func scanKiro(home string) []Found {
	path := kiroauth.CachePath(home)
	l, err := kiroauth.ImportKiroCache(home)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case errors.Is(err, kiroauth.ErrSocialLogin):
		return []Found{note("kiro", path, "social (GitHub/Google) Kiro login; sign in with `belai login kiro` instead")}
	case err != nil:
		return []Found{note("kiro", path, err.Error())}
	}
	return []Found{{
		Agent:    "kiro",
		Provider: "kiro",
		Field:    "login",
		Location: path,
		value:    l.Encode(),
	}}
}
