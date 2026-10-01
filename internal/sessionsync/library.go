package sessionsync

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// The agent library routes live beside the session routes under /v1/belai and
// share the client, so they inherit its origin allowlist, its credential
// handling and its response bounds unchanged. A host calls them only to answer
// a profile_backup or profile_install request, and the server accepts each call
// only for the request that names it, while it is delivered to this host.

// MaxLibraryProfile is the largest agent profile the library holds, in bytes.
const MaxLibraryProfile = 64 << 10

// LibraryBackup uploads a profile this host exported, answering the
// profile_backup request dispatchID, and returns the version the library
// holds it as.
func (c *Client) LibraryBackup(ctx context.Context, hostID, dispatchID, markdown string) (version string, err error) {
	if len(markdown) > MaxLibraryProfile {
		return "", fmt.Errorf("sessionsync: the profile is larger than %d bytes", MaxLibraryProfile)
	}
	var out struct {
		Version string `json:"version"`
	}
	err = c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/library/backups",
		map[string]string{"dispatch": dispatchID, "markdown": markdown}, &out, requestTimeout)
	return out.Version, err
}

// LibraryFetch reads the library version a profile_install request names,
// answering request dispatchID. The server serves that one version and nothing
// else for it.
func (c *Client) LibraryFetch(ctx context.Context, hostID, profileID, version, dispatchID string) (markdown string, err error) {
	var out struct {
		Markdown string `json:"markdown"`
	}
	path := fmt.Sprintf("/hosts/%s/library/profiles/%s/versions/%s?dispatch=%s",
		url.PathEscape(hostID), url.PathEscape(profileID), url.PathEscape(version), url.QueryEscape(dispatchID))
	if err := c.do(ctx, http.MethodGet, path, nil, &out, requestTimeout); err != nil {
		return "", err
	}
	if len(out.Markdown) > MaxLibraryProfile {
		return "", fmt.Errorf("sessionsync: the profile is larger than %d bytes", MaxLibraryProfile)
	}
	return out.Markdown, nil
}
