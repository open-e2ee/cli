# OpenE2EE CLI

`oe` initializes OpenE2EE projects, keeps public service policy in a deterministic
file, and provides the command boundary for managed development and deployment.

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
oe dev        create managed development and wait for its first acknowledged message
oe plan       show the production configuration change without applying it
oe deploy     complete the production card gate, confirm, and deploy
oe doctor     check config, credentials, and control-plane health
oe project    list, inspect, or select a project
oe provider   inspect or configure an advanced identity provider
oe secret     list names, set a value, or delete a service secret
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
