# OpenE2EE CLI

`oe` initializes OpenE2EE projects, keeps public service policy in a deterministic
file, and provides the command boundary for hosted Development and Production.

OpenE2EE Relay provides managed encrypted delivery for the OpenE2EE Signal
Protocol SDK. This public repository lets developers inspect the command,
config, credential, and release contracts.

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
oe init       initialize public policy and the local encrypted example
oe login      use browser authorization and store the result in the OS keychain
oe dev        create the Development environment, install its Relay connection, and wait for acknowledgement
oe plan       show the production configuration change without applying it
oe deploy     complete the production card gate, deploy, and install its Relay connection
oe doctor     check project, environment, connection, credentials, and control-plane health
oe project    inspect or select a project
oe notifications stage and verify best-effort notification profiles
```

Global `--json` emits one final JSON document. `--json-stream` emits
newline-delimited progress and final events. `--environment` is an advanced
override. Normal work uses development for `oe dev`. It uses production for
`oe plan` and `oe deploy`.

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

The config records the selected environment. Each selected environment stores one public `relayUrl`. `oe dev` writes the
development value to `.env.local`. `oe deploy` writes the production value to
`.env.production.local`. Both files use the semantic
`OPEN_E2EE_RELAY_URL` setting. The commands add both files to `.gitignore`. The application
does not select a Relay hostname or pair an endpoint with a second key.

`oe deploy` writes `.env.production.local`. If the hosting provider does not
read that file, install its `OPEN_E2EE_RELAY_URL` value in the production build
environment. The application source stays unchanged.

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

`@open-e2ee/cli` contains a small Node launcher and six optional native packages:
macOS, Linux, and Windows on arm64 and x64. Installation does not run a
postinstall download. The release workflow cross-compiles the Go command, creates
checksums and a CycloneDX SBOM, and creates GitHub build-provenance attestations
for every release artifact. Verify a published artifact with:

```bash
gh attestation verify PATH_TO_ARTIFACT -R open-e2ee/cli
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
