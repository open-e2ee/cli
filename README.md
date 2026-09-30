# OpenE2EE CLI

`oe` initializes OpenE2EE projects, keeps public service policy in a deterministic
file, and provides the command boundary for hosted Sandbox and Production.

The OpenE2EE Signal Protocol Relay provides hosted encrypted delivery, built to
work with the OpenE2EE Signal Protocol SDK. This public repository lets
developers inspect the command, config, credential, and release contracts.

## Local start

Build the command from source:

```bash
go build -o ./bin/oe ./cmd/oe
./bin/oe init --name my-chat
npm install @open-e2ee/signal-protocol-sdk
node open-e2ee-local.mjs
```

The initializer needs no login, card, or backend. It writes:

- `open-e2ee.jsonc`, which contains public desired service policy.
- `open-e2ee-local.mjs`, which runs real protocol and cryptography with the
  SDK's development-only in-memory adapters.

`npm create oe@latest` initializes the same files and runs the local encrypted
round trip.

## Command contract

```text
oe init                 initialize public policy and the local encrypted example
oe auth login           log in with a browser, store the session in the OS keychain, and accept the terms
oe auth status          show the user, the organization, the terms state, and the token source
oe auth logout          remove the stored session
oe sandbox              create the Sandbox environment, install its Relay connection, and wait for acknowledgement
oe plan                 show the production configuration change without applying it
oe deploy               complete the production card gate, deploy, and install its Relay connection
oe doctor               check project, environment, connection, credentials, and control-plane health
oe project show         read a project and the state of each environment
oe project connection   print the Relay connection URL of one environment
oe project select       select another project
oe notifications        stage and verify best-effort notification profiles
oe help [COMMAND]       show every command, or the usage of one command
```

`oe help` lists each command with its usage, the global flags, the exit codes,
and the environment variables. `oe help --json` returns the same surface as
data. `oe COMMAND --help` shows one command.

Global flags can come before or after the command. Global `--json` emits one
final JSON document, and `oe auth login` emits one pending event before it. `--json-stream` emits newline-delimited progress and final
events. `--agent yes|no|auto` says whether a coding agent runs `oe`.
`--environment` is an advanced override. Normal work uses sandbox for
`oe sandbox`. It uses production for `oe plan` and `oe deploy`.

## Use from a coding agent

A coding agent can do each task in this section with `oe` and no console page.

Install the command without a prompt:

```bash
npm install --global @open-e2ee/oe
oe --json version
```

`oe` detects a coding agent from the environment variables that the agent sets,
for example `CLAUDECODE` or `CODEX_THREAD_ID`. Under an agent, the default
output is JSON, and `oe` never prompts and never opens a browser. Pass
`--agent yes` for an agent that `oe` does not detect. `--agent no` restores
text output. In JSON mode, each run writes one JSON document to stdout for the
result, for success and for failure. `oe auth login` also writes one pending
event before the result (see below).

```json
{"status":"ok","command":"project","message":"...","data":{}}
{"status":"error","command":"plan","error":"...","code":"CONFIG_NOT_FOUND","next":"oe init"}
```

Switch on `code`, not on the text of `error`. When `next` is present, it is the
command that moves the task forward. When `action` is present, `action.url` is a
page that a person must open, so give it to the person. In text mode, a failure
goes to stderr as `error:`, `action:`, and `next:` lines, and stdout stays clean.

| Exit code | Meaning                                                                                            |
| --------- | -------------------------------------------------------------------------------------------------- |
| 0         | The command succeeded.                                                                             |
| 1         | The command failed. `code` tells why.                                                              |
| 2         | The command line is invalid, or a required input is missing, for example `--confirm`.              |
| 4         | Authentication is required. Run `oe auth login`.                                                   |
| 5         | A person must act. `error` tells what to do. When `action.url` is present, it is the page to open. |
| 6         | The failure is temporary. `next` is the same command. Run it again later.                          |

Log in once. A person must approve the login in a browser. Under an agent, `oe`
does not open the browser. It writes a pending event, then the result after the
person approves, so start the command in the background and give the person
`action.url` and `data.userCode`:

```bash
oe auth login
```

```json
{"status":"pending","command":"auth login","message":"A person must approve this device.","action":{"kind":"browser","url":"https://...","reason":"login"},"data":{"userCode":"ABCD-EFGH","expiresInSeconds":900}}
{"status":"ok","command":"auth login","message":"...","data":{"terms":"required","canAccept":true,"documents":[]},"next":"oe auth login --accept-terms"}
```

The organization accepts the terms once. When `data.terms` is `required`, show
the person the URL of each document in `data.documents`, and run
`oe auth login --accept-terms` only after the person agrees. With a stored
session, it starts no second login. When `canAccept` is false, an administrator
of the organization must accept, and `--accept-terms` fails with
`TERMS_PERMISSION_REQUIRED`, exit 5. A command that needs the terms fails with
`TERMS_REQUIRED`, exit 5, and `data.retry` is the command to run again after the
acceptance.

`oe auth status` shows the user, the organization, the terms state, and the
token source, and exits 4 without a session. Protected CI uses a scoped
`OE_ACCESS_TOKEN` instead of a login.

Read the Relay connection URL of a project. Text mode prints only the URL, so a
shell can capture it:

```bash
oe project connection --environment sandbox
oe project connection my-chat --environment production --json
```

In JSON, `data.variable` names the variable that the application reads (see
[Configuration ownership](#configuration-ownership)). A project with no active
environment fails with `ENVIRONMENT_NOT_ACTIVE`, and `next` names
`oe sandbox` or `oe deploy`.

A production deploy never waits for an answer that no person can give. Without
a terminal, under an agent, and in JSON and CI modes, `oe deploy` stops with
`CONFIRMATION_REQUIRED` and returns the plan in `data`. Review the changes, then
run `oe deploy --confirm`. When billing setup is incomplete, the deploy stops
with `BILLING_SETUP_REQUIRED`, and `action.url` is the page a person must
finish. `oe` opens that page in a browser only for a person at a terminal.

## Configuration ownership

`open-e2ee.jsonc` is public policy. It rejects unknown fields so a secret cannot
silently remain in the file. Secrets belong in the service secret store. Login
credentials belong in the operating-system keychain. Protected CI can supply a
scoped `OE_ACCESS_TOKEN`. The CLI keeps it in memory and never stores it.

Each project has one writer mode:

- `config` makes the repository the desired-state writer.
- `console` makes the console the desired-state writer.

Local mutation commands take an advisory project lock. Remote mutations include
a stable idempotency key. Plans and deploys include the server's expected
revision. A console-first project or a revision conflict fails closed.

The config records the selected environment. Each selected environment stores one public `relayUrl`. `oe sandbox` writes the
sandbox value to `.env.local`. `oe deploy` writes the production value to
`.env.production.local`. The commands add both files to `.gitignore`. The application
does not select a Relay hostname or pair an endpoint with a second key.

The variable follows the framework in `package.json`:

| Dependency                      | Variable                          |
| ------------------------------- | --------------------------------- |
| `next`                          | `NEXT_PUBLIC_OPEN_E2EE_RELAY_URL` |
| `expo`                          | `EXPO_PUBLIC_OPEN_E2EE_RELAY_URL` |
| `vite`                          | `VITE_OPEN_E2EE_RELAY_URL`        |
| Any other, or no `package.json` | `OPEN_E2EE_RELAY_URL`             |

The first match in the table wins. The CLI replaces only its own comment and
the lines that assign its variable. It keeps every other line, the line
breaks, an `export` prefix, and the file permissions.

`oe deploy` writes `.env.production.local`. If the hosting provider does not
read that file, install the value of the variable that `oe deploy` names in the
production build environment. The application source stays unchanged.

## iOS notification workflow

Push is a best-effort wake. The durable Relay mailbox, authenticated pull, and
acknowledgement are delivery authority.

```bash
oe notifications setup ios --profile background-only
oe notifications setup ios --profile visible-alert
oe notifications add-nse
oe notifications verify ios
oe notifications verify ios --app-bundle ./path/to/App.app
oe notifications apple-filtering-request
```

`setup ios` supports discretionary background wakes or generic visible alerts.
It does not put message content, ciphertext, identifiers, or receipt state in a
provider payload. Expo projects must use a development or native build. Expo Go
cannot verify remote push or contain a Notification Service Extension.

`add-nse` creates a generic, timeout-safe Notification Service Extension. Expo
CNG uses `@bacons/apple-targets`. Bare React Native receives the same source and
an exact Xcode target handoff. The extension does not get App Group or Keychain
access by default and does not decrypt a preview.

Apple's notification-filtering entitlement is separate from an NSE. It permits
an approved, signed extension to suppress an alert. It does not improve APNs
transport or guarantee execution. The CLI keeps filtering unavailable until
Apple approval, signed-build inspection, and physical-device suppression
evidence all pass. Simulator results are not physical-device evidence.

## Distribution contract

`@open-e2ee/oe` contains a small Node launcher and six optional native packages:
macOS, Linux, and Windows on arm64 and x64. Installation does not run a
postinstall download. The release workflow cross-compiles the Go command, creates
checksums and a CycloneDX SBOM, and creates GitHub build-provenance attestations
for every release artifact. Verify a published artifact with:

```bash
gh attestation verify PATH_TO_ARTIFACT -R open-e2ee/oe
```

The repository also contains a Homebrew formula for the native command.

## Development

Development needs Go 1.26 or later, Node 20 or later, and npm 11 or later.

```bash
go test -race ./...
go vet ./...
npm ci
npm test
npm run format:check
npm run homebrew:test
```

The CLI does not contain telemetry. The service measures managed activation from
the first authenticated control request and first acknowledged message. This
command does not use an analytics SDK.

## Security

See [SECURITY.md](./SECURITY.md). Do not open a public issue for a suspected
credential, authentication, authorization, or cryptographic defect.

## License

Apache-2.0. See [LICENSE](./LICENSE).
