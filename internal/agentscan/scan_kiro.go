package agentscan

import (
	"errors"
	"os"

	"github.com/vulnetix/belai/internal/kiroauth"
)

// scanKiro imports the AWS sign-in Kiro's IDE or CLI made (the IDE's AWS SSO
// cache, else kiro-cli's database) as the kiro provider's login. A GitHub or Google Kiro login refreshes through Kiro's
// own service and is reported, not imported.
func scanKiro(home string) []Found {
	l, path, err := kiroauth.ImportKiroSource(home)
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
