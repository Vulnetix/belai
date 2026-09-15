package run

import (
	"github.com/vulnetix/signet/internal/copilotauth"
)

// copilotExchanger trades GitHub OAuth tokens for Copilot session tokens
// inside buildRequest. It is a package variable so tests can substitute an
// httptest-backed exchanger.
var copilotExchanger = copilotauth.NewExchanger(nil)
