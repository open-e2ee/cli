# `@open-e2ee/oe`

This package installs the `oe` command through an optional native package for
the current operating system and CPU. It does not download a binary during
installation.

Run `oe auth login` to log in with a browser and accept the terms for the
organization. Use `oe sandbox` for the project's Sandbox environment or
`oe deploy` for Production.

Run `oe help` for every command, its usage, the exit codes, and the environment
variables. Under a coding agent, or with `--agent yes`, `oe` writes JSON by
default: each run writes one JSON document to stdout with a stable `code` and,
when there is one, the `next` command. Run
`oe project connection --environment sandbox` to print the Relay connection URL
of an environment.
