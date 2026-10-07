package libstore

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/mcp"
)

func init() { register(libitem.MCP, mcpStore{}) }

// mcpStore keeps an MCP server where Belai reads it: an entry of `mcp.servers` in
// the user's own settings.json. The document holds references and names, never a
// secret value: a cred:KEY value reads the credential Belai stored for this server
// (a push from the library, internal/rc, or the user in /mcp), so installing an
// item cannot overwrite or reveal a stored secret. The built-in servers are not
// items (a settings entry named clef is ignored), and a project settings file's
// servers are never read: only the user's own layer is synced.
type mcpStore struct{}

func (mcpStore) list() ([]Local, []Skipped, error) {
	s, err := config.LoadGlobal()
	if err != nil || s.MCP == nil {
		return nil, nil, err
	}
	var out []Local
	var skipped []Skipped
	names := make([]string, 0, len(s.MCP.Servers))
	for n := range s.MCP.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if strings.EqualFold(n, config.ClefMCPName) {
			continue
		}
		b, err := libitem.MCPDocument(n, s.MCP.Servers[n])
		if err != nil {
			skipped = append(skipped, Skipped{Kind: libitem.MCP, Name: n, Reason: err.Error()})
			continue
		}
		// A hand-written entry the library's rules refuse (a literal secret, a
		// plain http URL) is a stray to the library: say why, so it is not
		// silently left out of the sync.
		it, err := libitem.Validate(libitem.MCP, b)
		if err != nil {
			skipped = append(skipped, Skipped{Kind: libitem.MCP, Name: n, Reason: err.Error()})
			continue
		}
		out = append(out, local(libitem.MCP, it.Name, it.Doc))
	}
	return out, skipped, nil
}

func (mcpStore) install(it libitem.Item, o InstallOptions) (Result, error) {
	d, err := libitem.ParseMCP(it.Doc)
	if err != nil {
		return Result{}, err
	}
	path, err := config.GlobalSettingsPath()
	if err != nil {
		return Result{}, err
	}
	replaced := false
	err = config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		if s.MCP == nil {
			s.MCP = &config.MCPSettings{}
		}
		if s.MCP.Servers == nil {
			s.MCP.Servers = map[string]config.MCPServer{}
		}
		// The library folds case: Acme and acme are one server.
		for existing := range s.MCP.Servers {
			if strings.EqualFold(existing, d.Name) {
				if !o.Overwrite {
					return ErrExists
				}
				delete(s.MCP.Servers, existing)
				replaced = true
			}
		}
		s.MCP.Servers[d.Name] = d.MCPServer
		return validateWritten(*s)
	})
	if err != nil {
		return Result{}, err
	}
	// A running Belai (the rc daemon, a TUI) starts it now; the next session
	// built carries its tools. A server that cannot start shows as failed in /mcp.
	if m := mcp.Active(); m != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			_ = m.Upsert(ctx, d.Name, d.MCPServer)
		}()
	}
	return Result{Replaced: replaced, Where: path + " (mcp.servers." + d.Name + ")"}, nil
}
