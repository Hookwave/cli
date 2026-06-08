# Hookwave CLI

Official command-line interface for [Hookwave](https://hookwave.dev) — the reliable webhook gateway for AI agents and automation builders.

## Install

### macOS / Linux (Homebrew)

```sh
brew install hookwave/cli/hookwave
```

### Other platforms

Download a pre-built binary from the [latest release](https://github.com/Hookwave/cli/releases/latest) and put it on your `PATH`.

### From source (Go 1.22+)

```sh
go install github.com/hookwave/cli/cmd/hookwave@latest
```

## Quickstart

```sh
hookwave login              # OAuth-style device-code flow in your browser
hookwave whoami             # confirm the org you're signed in to
hookwave sources list       # list your inbound webhook sources
hookwave events list        # recent events
hookwave doctor <event-id>  # diagnose a failed delivery
hookwave listen 3000        # tunnel a public ingest URL to localhost:3000
hookwave mcp                # MCP server for Claude Desktop / Cursor / Continue
```

See `hookwave --help` for the full command tree, or read [docs.hookwave.dev](https://docs.hookwave.dev/cli).

## MCP integration

`hookwave mcp` boots a Model Context Protocol server over stdio so AI clients can list events, mint SDK keys, replay deliveries, diagnose failures, and more. Drop this into your client config:

```json
{
  "mcpServers": {
    "hookwave": {
      "command": "hookwave",
      "args": ["mcp"]
    }
  }
}
```

Full tool catalogue: [docs.hookwave.dev/mcp](https://docs.hookwave.dev/mcp).

## License

MIT — see [LICENSE](./LICENSE).
