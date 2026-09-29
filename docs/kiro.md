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
belai login kiro -start-url https://acme.awsapps.com/start -region ap-southeast-2
belai login kiro -import               # reuse the sign-in Kiro already made
```

Belai prints a verification URL and a code. Open the URL, confirm the code,
and approve the request; Belai polls until AWS answers and then stores the
login.

| Flag | Meaning |
| --- | --- |
| `-start-url` | IAM Identity Center start URL. Omit it for an AWS Builder ID. |
| `-region` | The SSO region of that start URL. Any AWS region works; the default is `us-east-1`, the Builder ID region. |
| `-api-region` | The Kiro API region. The default is the profile's region, else the SSO region. |
| `-profile-arn` | The CodeWhisperer profile to use. By default Belai looks it up after signing in (see [Profiles](#profiles)). |
| `-backend` | `keychain` or `user-file`. The default is the keychain when one works, else the user credentials file. The macOS keychain holds about 3000 bytes per item, which an Identity Center login can pass: with the default, such a login goes to the user file and the CLI says so; with `-backend keychain` it fails. |
| `-import` | Import Kiro's own sign-in from `~/.aws/sso/cache` instead of signing in. |

After signing in, run with `-provider kiro`, or pick Kiro in `/providers`.

### TUI

Open `/providers`, choose **kiro**, and press `a` on the credentials tab.
Press enter on an empty line to sign in with an AWS Builder ID, or type an
Identity Center start URL and, optionally, its region. The verification URL
opens in the browser and the code is shown in the view. Press `x` to cancel a
sign-in that is still waiting. When the account has several profiles, the tab
lists them: choose one with ↑↓ and enter, or press esc to store the login
without one. The login is stored in the backend the tab shows; `b` cycles it.

### Profiles

After a sign-in or an import, Belai asks CodeWhisperer which profiles the
account can use. The call is `ListAvailableProfiles` on
`codewhisperer.<region>.amazonaws.com`, falling back to `q.<region>` if that
fails. It asks the login's own regions first (the API region, then the SSO
region) and then `us-east-1` and `eu-central-1`. So an Identity Center
instance in, say, `ap-southeast-2` finds its profile in its own region.

- **One profile.** Its ARN is stored in the login, and its region becomes the
  Kiro API region.
- **Several profiles.** The CLI lists them and asks on a terminal. Without a
  terminal it stops and asks for `-profile-arn`.
- **No profile, or a failed lookup.** A Builder ID account may have none. The
  login is stored without a profile and the CLI prints a warning.

`-profile-arn` skips the lookup; the ARN must be a well-formed CodeWhisperer
ARN. An imported sign-in that already names Kiro's profile skips it too, so
an account that answers the lookup with HTTP 403 still imports. If the lookup's own token refresh rotates the refresh token, Belai stores
the rotated one.

### Importing Kiro's sign-in

The Kiro IDE keeps its AWS sign-in in
`~/.aws/sso/cache/kiro-auth-token.json`, next to a client registration file
named by the hash of its client ID. `kiro-cli` keeps its sign-in in a SQLite
database instead: `~/Library/Application Support/kiro-cli/data.sqlite3` on
macOS, `~/.local/share/kiro-cli/data.sqlite3` (or under `$XDG_DATA_HOME`) on
Linux, with the token and client registration in its `auth_kv` table and an
Identity Center profile in its `state` table. Both `belai login kiro -import`
and the `i` import view (which lists it as agent `kiro`) read the IDE's files
first and fall back to kiro-cli's database.

Belai reads the database with the system `sqlite3` (read-only, a fixed
command, the scrubbed environment). macOS always has it; on Linux, install
it or sign in with `belai login kiro`.

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

Kiro is reached through `POST https://q.<api-region>.amazonaws.com/generateAssistantResponse`.
The API region is the profile's region, else the SSO region the login was made
in, else `us-east-1`. The request is sent
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

- `metadataEvent` with `tokenUsage`: its counts become the turn's usage. Prompt
  tokens are uncached input plus cache reads plus cache writes, and completion
  tokens are the output tokens. A later event replaces an earlier one rather
  than adding to it.
- `contextUsageEvent`, the fallback when no token counts arrive. The
  percentage of the model's input limit becomes the prompt-token estimate. It
  is skipped when the limit is unknown.

Metering (credit) events are ignored.

## Models, effort and images

`internal/kiromodels` reads the account's catalogue with
`GET /ListAvailableModels?origin=AI_EDITOR` (plus `profileArn` when the login
has one). It follows up to five pages and caches the result for five minutes.
The `/providers` model tab and the model picker show this list, with each
model's context window and output limit. The static catalogue remains the
fallback.

Kiro checks `additionalModelRequestFields` against a JSON schema that each
model publishes, and rejects any property the schema does not declare. Belai
therefore reads each model's schema and sends only what it declares:

- **Effort.** Claude models take `output_config.effort`; GPT models take
  `reasoning.effort`. Effort is sent only when the chosen level is in the
  model's declared list. The picker offers exactly those levels.
- **`max_tokens`.** Sent only when a cap is set and the schema declares
  `max_tokens`. The cap is held within the model's minimum and maximum.
- **No schema.** A model without one gets no extra fields, so a missing or
  unreadable schema falls back to the plain request instead of a 400.

A headless run that never opened the picker fetches the catalogue once, with
the same token as the request. A failed fetch is remembered as empty for five
minutes, so it isn't retried on every request.

**Images.** The Kiro encoder sends image attachments as
`userInputMessage.images`, each `{format, source: {bytes}}`:

- formats png, jpeg, gif and webp, at most 3.75 MB each;
- only on the current message, because a request that replays earlier images
  is rejected;
- only when the model lists `IMAGE` among its input types. Otherwise the
  images are dropped and a harness note says so.

Egress never folds image bytes into the text. Nothing in Belai creates an
image attachment yet (see [Image attachments](image-attachments.md)), so for
now this is wire support only.

## Limits

- Kiro is never routed through the Vulnetix AI Firewall. Its tokens are
  minted from an AWS sign-in, and the gateway does not relay its surface.
- Kiro logins made with GitHub or Google are not supported.
- These API shapes are not documented by AWS. They follow what Kiro's own
  clients send, and every parser treats an unknown shape as "not reported"
  rather than as an error.

## Security

- Tokens go only to pinned hosts, over https. The SSO-OIDC calls never follow
  a redirect:
  - SSO-OIDC: `oidc.<region>.amazonaws.com`.
  - Kiro API, model list and profile lookup: `q.<region>.amazonaws.com` or
    `codewhisperer.<region>.amazonaws.com`. These calls never follow a
    redirect either.

  A region must look like an AWS region name, so a hand-edited login cannot
  steer those hosts. `BELAI_BASE_URL` may point Kiro only at one of those
  hosts, or at a loopback address for tests and local mocks.
- The device code, client secret, refresh token and access token never
  reach a transcript, log, setting, notification or model. The TUI shows the
  user code and the verification URL only, and error text from AWS is reduced
  to its error code.
- The model catalogue is kept as facts only: IDs, limits, effort levels and the
  image flag. Model names are reduced to printable text, and IDs and effort
  values to identifier characters.
- The `kiro` auth style is reserved for the built-in provider. A custom
  profile cannot claim it.
- The Kiro cache import reads two fixed files, with a size cap. The client
  registration's file name must be a plain hash, so a hostile
  `clientIdHash` cannot traverse out of the cache directory.
