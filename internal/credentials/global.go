package credentials

import (
	"os"

	"github.com/vulnetix/belai/internal/config"
)

// NewGlobalResolver builds a resolver over the user's own layers only: the global
// settings, the user credentials file, .netrc and the keychain. It reads no project
// settings and no project credentials file, so it behaves the same in any working
// directory. It is for a caller that stores a credential on the user's behalf and
// is not running in a project (the rc daemon storing a provider key the website
// sent).
func NewGlobalResolver() (*Resolver, error) {
	userPath, err := config.UserCredentialsPath()
	if err != nil {
		return nil, err
	}
	settings, err := config.LoadGlobal()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return &Resolver{
		env:      os.Getenv,
		home:     home,
		settings: settings,
		userFile: newFileStore(userPath, false),
		// No project file: an empty path never exists and is never written.
		projFile:         newFileStore("", true),
		netrc:            newNetrcStore(),
		keychain:         newKeyringBackend(),
		vulnetixKeychain: NewKeyringBackend("vulnetix"),
	}, nil
}
