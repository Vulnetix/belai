# Kiro

The built-in `kiro` provider runs Belai on a Kiro subscription. Kiro has no
API key: it signs in through AWS, either with an **AWS Builder ID** (the free
personal sign-in) or with an **IAM Identity Center** start URL (an
organisation's Kiro seats). Belai runs that sign-in itself, stores the result
as the provider's one credential field, `login`, and trades it for a
short-lived access token before each model request.

## Signing in

### CLI

```sh
belai login kiro                       # AWS Builder ID
belai login kiro -start-url https://acme.awsapps.com/start -region eu-west-1
belai login kiro -import               # reuse the sign-in Kiro already made
```

Belai prints a verification URL and a code. Open the URL, confirm the code,
and approve the request; Belai polls until AWS answers and then stores the
login.

| Flag | Meaning |
| --- | --- |
| `-start-url` | IAM Identity Center start URL. Omit it for an AWS Builder ID. |
| `-region` | The SSO region of that start URL (default `us-east-1`). |
| `-api-region` | The Kiro API region (default `us-east-1`). |
| `-profile-arn` | The CodeWhisperer profile ARN that an Identity Center account sends with each request. |
| `-backend` | `keychain` or `user-file`. The default is the keychain when one works, else the user credentials file. |
| `-import` | Import Kiro's own sign-in from `~/.aws/sso/cache` instead of signing in. |

After signing in, run with `-provider kiro`, or pick Kiro in `/providers`.

### TUI

Open `/providers`, choose **kiro**, and press `a` on the credentials tab.
Press enter on an empty line to sign in with an AWS Builder ID, or type an
Identity Center start URL and, optionally, its region. The verification URL
opens in the browser and the code is shown in the view. Press `x` to cancel a
sign-in that is still waiting. The login is stored in the backend the tab
shows; `b` cycles it.

### Importing Kiro's sign-in

Kiro's IDE and CLI keep their AWS sign-in in
`~/.aws/sso/cache/kiro-auth-token.json`, next to a client registration file
named by the hash of its client ID. Both `belai login kiro -import` and the
`i` import view (which lists it as agent `kiro`) read those two files.

Only AWS Builder ID and Identity Center logins can be imported. A Kiro login
made with GitHub or Google refreshes through Kiro's own auth service rather
than AWS SSO-OIDC, so the import view reports it without importing it. Sign
in with `belai login kiro` instead.

## Tokens

The stored `login` is a JSON object that holds:

- the refresh token;
- the SSO-OIDC client registration: client ID, client secret and its expiry;
- the SSO region and start URL;
- the optional API region and profile ARN.

The whole object is secret. `KIRO_LOGIN` can supply the same value from the
environment.

Before each request, the refresher calls SSO-OIDC `CreateToken` with the
refresh token. It caches the access token until two minutes before it
expires, and concurrent requests share a single refresh. When AWS rotates the
refresh token, Belai uses the new one for the rest of the process. It also
writes the new login back to the backend that held the old one, but only if
that backend is the keychain or a credentials file and the value there has not
changed since it was read. It never rewrites a value that came from the
environment, an env reference or netrc.

In two cases Belai stops and asks you to run `belai login kiro` again:

- AWS rejects the refresh token (`invalid_grant`, `invalid_client`, or a
  401/403);
- the client registration has expired (about 90 days after sign-in).

## Wire surface

Kiro is reached through `POST https://q.<api-region>.amazonaws.com/generateAssistantResponse`
with `Authorization: Bearer <access token>`. The reply is binary AWS
event-stream frames, not SSE. `internal/wire/eventstream.go` reads them and
checks both CRCs of every frame. A frame over 1 MiB, or one whose checksum
fails, ends the stream with an error; it is never skipped.

The service has no system role and no tool role, so Belai adapts the
conversation (`internal/run/kiro.go`):

- The sealed system prompt leads the first user message. It still passes
  through the same egress checks as on every other provider.
- Tool results ride in `userInputMessageContext.toolResults` on the user
  message after the assistant's `toolUses`.
- Consecutive turns from the same role merge, so the history strictly
  alternates between user and assistant, and the newest message is always the
  user's.
- Tools are advertised on the current message only.
- The conversation ID is derived from the opening message, so it stays the
  same across the requests of one conversation.

The events Belai reads are:

- `assistantResponseEvent`, which becomes text.
- `toolUseEvent`, whose fragments are accumulated by `toolUseId` and
  completed on `stop`. A repeat of a finished tool use is ignored.
- An `exception` or `error` frame, which ends the turn with the exception
  type and a flattened, length-capped message.

Metering and context-usage events are ignored. Kiro reports no token counts.

## Limits

- Models come from a static catalogue: `auto`, `claude-sonnet-4.5` (the
  default), `claude-sonnet-4`, `claude-haiku-4.5` (the fast tier) and
  `claude-opus-4.5`. There is no live model list.
- Kiro is never routed through the Vulnetix AI Firewall. Its tokens are
  minted from an AWS sign-in, and the gateway does not relay its surface.
- Effort and extended-thinking settings are not sent.
- Image attachments are not sent. Only the text of each turn reaches Kiro.

## Security

- Tokens go only to pinned hosts, over https. The SSO-OIDC calls never follow
  a redirect:
  - SSO-OIDC: `oidc.<region>.amazonaws.com`.
  - Kiro API: `q.<region>.amazonaws.com` or
    `codewhisperer.<region>.amazonaws.com`.

  A region must look like an AWS region name, so a hand-edited login cannot
  steer those hosts. `BELAI_BASE_URL` may point Kiro only at one of those
  hosts, or at a loopback address for tests and local mocks.
- The device code, client secret, refresh token and access token never
  reach a transcript, log, setting, notification or model. The TUI shows the
  user code and the verification URL only, and error text from AWS is reduced
  to its error code.
- The `kiro` auth style is reserved for the built-in provider. A custom
  profile cannot claim it.
- The Kiro cache import reads two fixed files, with a size cap. The client
  registration's file name must be a plain hash, so a hostile
  `clientIdHash` cannot traverse out of the cache directory.
