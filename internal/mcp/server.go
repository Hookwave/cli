// Package mcp implements an MCP (Model Context Protocol) server
// that exposes Hookwave operations as tools for AI assistants.
//
// Transport: stdio. Configure in Claude Desktop / Cursor / Continue
// by adding to the MCP config file:
//
//   {
//     "mcpServers": {
//       "hookwave": {
//         "command": "hookwave",
//         "args": ["mcp"]
//       }
//     }
//   }
//
// The server reads HOOKWAVE_TOKEN (or, if absent, the OS keyring) so
// the user doesn't have to put their CLI token in the AI client's
// config file. HOOKWAVE_API can override the API base for dev.
//
// Tool surface is intentionally narrow: heavy on read tools (the AI
// is mostly *answering questions*) and one carefully-scoped write
// tool (replay_event) plus two creates. Updates/deletes are
// deliberately *not* exposed — those should be explicit human
// actions, not LLM-driven side effects.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/hookwave/hookwave/apps/cli/internal/httpc"
)

// Run starts the MCP server over stdio. Returns when stdin closes
// (the AI client disconnects) or ctx is cancelled.
func Run(ctx context.Context, c *httpc.Client, version string) error {
	s := server.NewMCPServer(
		"hookwave",
		version,
		server.WithToolCapabilities(true),
	)

	registerTools(s, c)
	return server.ServeStdio(s)
}

func registerTools(s *server.MCPServer, c *httpc.Client) {
	s.AddTool(
		mcpgo.NewTool("hookwave_whoami",
			mcpgo.WithDescription("Returns the currently-authenticated user, org, plan, and feature limits."),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolWhoami(c),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_list_events",
			mcpgo.WithDescription("List recent events. Filter by status (queued|delivering|delivered|failed|dropped) or sourceId (UUID of a source in your org)."),
			mcpgo.WithString("status",
				mcpgo.Description("Optional status filter"),
				mcpgo.Enum("queued", "delivering", "delivered", "failed", "dropped"),
			),
			mcpgo.WithString("sourceId", mcpgo.Description("Optional source UUID")),
			mcpgo.WithNumber("limit",
				mcpgo.Description("Max rows (default 25, max 200)"),
				mcpgo.Min(1),
				mcpgo.Max(200),
			),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolListEvents(c),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_get_event",
			mcpgo.WithDescription("Fetch full event detail including delivery attempts and per-connection status."),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Event UUID")),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolGetEvent(c),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_doctor",
			mcpgo.WithDescription("Diagnose why an event failed. Fetches the event plus delivery attempts and surfaces a hint string based on the failure pattern. Read-only — no retries or mutations triggered."),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Event UUID to diagnose")),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolDoctor(c),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_replay_event",
			mcpgo.WithDescription("Re-queue an event for delivery. The event must be in a terminal state (delivered, failed, dropped) — not queued/delivering. The original source (Stripe, GitHub, etc.) is NOT re-triggered; the stored event is replayed."),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Event UUID")),
			// The one true mutation in the MCP surface: re-queues
			// delivery attempts. Idempotent in the sense that the same
			// event can be replayed repeatedly without corruption, but
			// each call WILL produce new delivery attempts and POST to
			// the destination — so we leave destructiveHint on so AI
			// clients prompt for approval.
			mcpgo.WithReadOnlyHintAnnotation(false),
			mcpgo.WithDestructiveHintAnnotation(true),
			mcpgo.WithIdempotentHintAnnotation(false),
		),
		toolReplayEvent(c),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_list_sources",
			mcpgo.WithDescription("List inbound webhook sources (Stripe, GitHub, etc.) configured in the active org."),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolListResource(c, "/v1/sources"),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_list_destinations",
			mcpgo.WithDescription("List outbound delivery targets (HTTP, Slack, Discord, etc.) in the active org."),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolListResource(c, "/v1/destinations"),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_list_connections",
			mcpgo.WithDescription("List connections — the Source → Destination wiring with filters / transformations."),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolListResource(c, "/v1/connections"),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_list_issues",
			mcpgo.WithDescription("List open / triaging / resolved issues. Issues are auto-grouped delivery problems (consecutive failures, source silent, schema drift, etc.)."),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolListResource(c, "/v1/issues"),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_get_issue",
			mcpgo.WithDescription("Fetch full issue detail: status, severity, description, comments, linked events, resolution notes, acknowledgement state."),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Issue UUID")),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolGetIssue(c),
	)

	// Write tools — AI clients (Claude Desktop, Cursor) prompt the
	// user to approve each tool call before it executes, so the
	// hallucination blast radius is bounded. We expose creates only;
	// updates and deletes stay CLI-only.
	s.AddTool(
		mcpgo.NewTool("hookwave_create_destination",
			mcpgo.WithDescription("Create an outbound delivery target. Type http needs a URL; type mock is for testing."),
			mcpgo.WithString("name", mcpgo.Required(), mcpgo.Description("Human-readable name; must be unique in the org.")),
			mcpgo.WithString("destinationType",
				mcpgo.Required(),
				mcpgo.Description("Destination type. http/n8n/make/slack/teams/discord need an https URL; telegram needs a Bot API sendMessage URL; email needs an address; klaviyo needs https; mock/cli/s3/twilio/postgres derive their target from typed config and ignore destinationUrl."),
				mcpgo.Enum("http", "n8n", "make", "slack", "teams", "discord", "telegram", "email", "klaviyo", "cli", "s3", "twilio", "postgres", "mock"),
			),
			mcpgo.WithString("destinationUrl", mcpgo.Description("Required for http/n8n/make/slack/teams/discord/telegram/email/klaviyo. Ignored for mock/cli/s3/twilio/postgres (those use typed config).")),
			mcpgo.WithReadOnlyHintAnnotation(false),
			mcpgo.WithDestructiveHintAnnotation(true),
		),
		toolCreateDestination(c),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_create_connection",
			mcpgo.WithDescription("Wire a source to a destination. Both must already exist (use list_sources / list_destinations to find ids, or create_destination first)."),
			mcpgo.WithString("name", mcpgo.Required(), mcpgo.Description("Human-readable name for this connection.")),
			mcpgo.WithString("sourceId", mcpgo.Required(), mcpgo.Description("UUID of the source.")),
			mcpgo.WithString("destinationId", mcpgo.Required(), mcpgo.Description("UUID of the destination.")),
			mcpgo.WithReadOnlyHintAnnotation(false),
			mcpgo.WithDestructiveHintAnnotation(true),
		),
		toolCreateConnection(c),
	)
}

// --- handlers ---------------------------------------------------------------

func toolWhoami(c *httpc.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		var r map[string]any
		if err := c.Get(ctx, "/v1/me", &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText(jsonString(r)), nil
	}
}

func toolListEvents(c *httpc.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		args := req.GetArguments()
		q := url.Values{}
		if v, ok := args["status"].(string); ok && v != "" {
			q.Set("status", v)
		}
		if v, ok := args["sourceId"].(string); ok && v != "" {
			// API events route uses snake_case query params; the MCP
			// tool keeps camelCase for its public interface. Translate
			// here. Previously this was silently dropped — the API
			// would ignore the unknown `sourceId` key and return
			// unfiltered events with no error. (Caught in docs round 2.)
			q.Set("source_id", v)
		}
		if v, ok := args["limit"].(float64); ok && v > 0 {
			q.Set("limit", fmt.Sprintf("%d", int(v)))
		}
		path := "/v1/events"
		if s := q.Encode(); s != "" {
			path += "?" + s
		}
		var r map[string]any
		if err := c.Get(ctx, path, &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText(jsonString(r)), nil
	}
}

func toolGetEvent(c *httpc.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, err := requiredString(req, "id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		var r map[string]any
		if err := c.Get(ctx, "/v1/events/"+url.PathEscape(id), &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText(jsonString(r)), nil
	}
}

func toolDoctor(c *httpc.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, err := requiredString(req, "id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		// Reuse the same /v1/events/<id> endpoint and run our doctor
		// pattern matcher. The CLI doctor command's analyseEvent lives
		// in internal/cli; rather than import it (cycle risk), call
		// the same endpoint and let the AI summarize. The CLI's
		// pattern catalogue is still the value-add for human use; for
		// AI use, structured raw data plus the prompt is plenty.
		var r struct {
			Data map[string]any `json:"data"`
		}
		if err := c.Get(ctx, "/v1/events/"+url.PathEscape(id), &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		// Wrap with a hint about what the AI should do with this data.
		out := map[string]any{
			"event":     r.Data,
			"hint": "Inspect status, deliveries[].responseStatus, deliveries[].errorMessage. Common patterns: 401/403 → outbound HMAC mismatch; 408/timeout error → handler too slow; DNS errorMessage → wrong destination URL; consistent 5xx → receiver bug.",
		}
		return mcpgo.NewToolResultText(jsonString(out)), nil
	}
}

func toolReplayEvent(c *httpc.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, err := requiredString(req, "id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		var r map[string]any
		if err := c.Post(ctx, "/v1/events/replay", map[string]any{"event_ids": []string{id}}, &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText("Replayed event " + id + ". " + jsonString(r)), nil
	}
}

func toolGetIssue(c *httpc.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, err := requiredString(req, "id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		var r map[string]any
		if err := c.Get(ctx, "/v1/issues/"+url.PathEscape(id), &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText(jsonString(r)), nil
	}
}

func toolCreateDestination(c *httpc.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := requiredString(req, "name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		dtype, err := requiredString(req, "destinationType")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		args := req.GetArguments()
		body := map[string]any{"name": name, "destinationType": dtype}
		if u, ok := args["destinationUrl"].(string); ok && u != "" {
			body["destinationUrl"] = u
		} else if dtype != "mock" {
			return mcpgo.NewToolResultError("destinationUrl is required unless destinationType is 'mock'"), nil
		}
		var r map[string]any
		if err := c.Post(ctx, "/v1/destinations", body, &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText("Created destination. " + jsonString(r)), nil
	}
}

func toolCreateConnection(c *httpc.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := requiredString(req, "name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		srcID, err := requiredString(req, "sourceId")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		destID, err := requiredString(req, "destinationId")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		body := map[string]any{
			"name":          name,
			"sourceId":      srcID,
			"destinationId": destID,
		}
		var r map[string]any
		if err := c.Post(ctx, "/v1/connections", body, &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText("Created connection. " + jsonString(r)), nil
	}
}

func toolListResource(c *httpc.Client, path string) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		var r map[string]any
		if err := c.Get(ctx, path, &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText(jsonString(r)), nil
	}
}

// --- helpers ----------------------------------------------------------------

func requiredString(req mcpgo.CallToolRequest, key string) (string, error) {
	args := req.GetArguments()
	v, ok := args[key].(string)
	if !ok || v == "" {
		return "", errors.New("missing required argument: " + key)
	}
	return v, nil
}

func jsonString(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("(failed to encode: %v)", err)
	}
	return string(b)
}
