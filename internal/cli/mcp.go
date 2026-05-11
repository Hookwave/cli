package cli

import (
	"github.com/spf13/cobra"

	"github.com/hookwave/hookwave/apps/cli/internal/mcp"
)

// `hookwave mcp` runs an MCP (Model Context Protocol) server over
// stdio. Designed to be launched by AI clients like Claude Desktop,
// Cursor, and Continue. Reads HOOKWAVE_TOKEN / keyring for auth and
// HOOKWAVE_API for the API base, so the AI client config doesn't
// need any secrets.
//
// Configure in Claude Desktop's mcp config:
//
//   {
//     "mcpServers": {
//       "hookwave": {
//         "command": "hookwave",
//         "args": ["mcp"]
//       }
//     }
//   }
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run a Model Context Protocol server (for Claude Desktop / Cursor / Continue)",
		Long: `Starts an MCP server over stdio, exposing Hookwave operations as
tools that AI assistants can call.

Tools (read):
  hookwave_whoami
  hookwave_list_events       (filter: status, sourceId, limit)
  hookwave_get_event         (id)
  hookwave_doctor            (id)  — diagnose a failed event
  hookwave_list_sources
  hookwave_list_destinations
  hookwave_list_connections
  hookwave_list_issues

Tools (action):
  hookwave_replay_event      (id)  — re-queue without re-triggering source

CRUD-style writes (create/update/delete) are intentionally NOT exposed
to the AI. Use the regular CLI for those.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			return mcp.Run(cmd.Context(), c, a.build.Version)
		},
	}
}
