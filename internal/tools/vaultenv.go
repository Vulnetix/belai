package tools

import (
	"os"
	"time"

	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/vaultenv"
)

// VaultCommandEnv is the environment for a command the model runs with the
// vault's grants (full Bash, a background command, a cloud native tool):
//
//   - the launch environment with the harness's credentials removed, except
//     that a name the vault grants at now is kept, so a vault-granted
//     AWS_SESSION_TOKEN is not dropped for ending in _TOKEN;
//   - then the vault's variables, which win over the launch environment's;
//   - then, when the vault holds a GitHub token, the git credential helper for
//     github.com (forge.RemoteAuthEnv), so model-run git can clone and push a
//     private repository.
//
// Read-only Bash never takes it.
func VaultCommandEnv(now time.Time) []string {
	env := proc.ScrubEnvKeeping(os.Environ(), vaultenv.Default.Names(now))
	env = append(env, vaultenv.Default.Environ(now)...)
	return forge.RemoteAuthEnv(env, now)
}
