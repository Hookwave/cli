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

	"github.com/hookwave/cli/internal/httpc"
)

// Run starts the MCP server over stdio. Returns when stdin closes
// (the AI client disconnects) or ctx is cancelled.
func Run(ctx context.Context, c *httpc.Client, version string) error {
	s := server.NewMCPServer(
		"hookwave",
		version,
		server.WithToolCapabilities(true),
		server.WithInstructions(serverInstructions),
	)

	registerTools(s, c)
	return server.ServeStdio(s)
}

// serverInstructions is surfaced to the AI client at session init and
// describes the Hookwave data model + common foot-guns so the AI stops
// reasoning from generic webhook/messaging-API priors. Keep it tight —
// long primers get truncated by some clients and dilute attention.
const serverInstructions = `Hookwave is a webhook router. The data model:

  source  →  connection  →  destination
  (inbound)     (rules + format)   (outbound)

A SOURCE accepts events. The provider drives auth:

  • stripe / shopify / github / replicate / lemonsqueezy / twilio:
    public ingest URL, signature-verified per provider.
  • generic: public ingest URL, no signature.
  • schedule: internal cron-triggered, no HTTP ingest.
  • code: SDK-ONLY. Public ingest URL is REJECTED. Events must arrive
    via /v1/ingest/batch with a Bearer src_live_… token issued from the
    source's SDK Keys section. Always pick this when the user is emitting
    events from their own code/SDK. It's the secure default for outbound
    SDK traffic — leaked ingest URLs can't trigger destinations.

A DESTINATION is where events leave Hookwave. CRITICAL: typed destinations
(twilio, whatsapp, postgres, s3) carry their full delivery config on the
destination row at creation time — Twilio destinations store from/to/channel
in twilioConfig, S3 stores bucket/region/key-template, Postgres stores
host/database/table. Events do NOT and SHOULD NOT carry per-event recipient
info for these types. If a user asks for a script that emits to a Twilio
destination, do NOT include a "to" field in the event payload — the worker
reads twilioConfig.to from the destination, not the event.

A CONNECTION links one source to one destination. The connection owns the
outbound format template (Jinja-like, references event.payload.* fields)
and retry rules. To know what fields an outbound message will contain, you
need the connection's format template — NOT just the event payload shape.

How an event flows:
  1. App POSTs JSON to source.ingestUrl  →  Hookwave stores it as an event
  2. Worker finds matching connections    →  applies the connection's filter rules
  3. For each match: connection template renders the outbound body  →  worker
     calls the destination's typed delivery handler (HTTP POST, Twilio API,
     S3 PUT, Postgres INSERT, etc.) using destination-row config

Practical guidance when generating SDK / script code for the user:

  • If the user wants to emit events from their own application,
    create the source with provider=code FIRST, then call
    hookwave_generate_source_key to mint an SDK key. The key is shown
    ONCE — paste it into the user's .env. Plain webhook ingest URLs
    don't apply: code sources reject anonymous POSTs.
  • Before writing code that sends to a destination, call
    hookwave_get_destination to inspect its config. Don't invent fields.
  • If the user has no connection or the connection's format template is
    raw passthrough, the destination receives the literal JSON payload.
    Recommend they set a format template (Format tab on the connection)
    if their downstream expects a specific shape.
  • For Twilio/WhatsApp/SMS: event body just needs the message text
    (typically as {"body": "..."} matching the template). No recipient,
    no auth token, no Twilio SID in the event.
  • For HTTP destinations with bearer/api-key auth: auth is on the
    destination, not the event. Don't re-pass credentials per event.

Write tools (create_source / create_destination / create_connection /
generate_source_key) ARE write tools — they prompt the user for approval
per the MCP destructive-hint contract. Use them when the user explicitly
asks to "set up" or "create"; don't auto-fire them mid-explanation.`

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
		mcpgo.NewTool("hookwave_get_source",
			mcpgo.WithDescription("Fetch full source detail including provider, ingestUrl, verification settings, rate limits, and schedule config. Use this BEFORE generating any code that emits events into a source — the ingestUrl and any allowedMethods restriction matter."),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Source UUID")),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolGetByPath(c, "/v1/sources/"),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_get_destination",
			mcpgo.WithDescription("Fetch full destination detail including destinationType, destinationUrl, and typed config (twilioConfig.to/from, s3Config.bucket, postgresConfig.host, whatsappConfig.recipientId, etc.). ALWAYS call this before writing code that emits to a destination so the AI knows what's already baked in (e.g. don't pass `to` in the event payload when twilioConfig.to is already set)."),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Destination UUID")),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolGetByPath(c, "/v1/destinations/"),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_get_connection",
			mcpgo.WithDescription("Fetch full connection detail including filter rules, retry policy, and outbound format template. The format template determines what fields the destination actually receives — generic event payloads are rewritten by this template before delivery. Useful when writing event-emitting code, because the user's downstream system (Slack message, Twilio body, etc.) sees the rendered template, not the raw event."),
			mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Connection UUID")),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolGetByPath(c, "/v1/connections/"),
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
		mcpgo.NewTool("hookwave_create_source",
			mcpgo.WithDescription("Create an inbound source. Provider sets the auth model: stripe/shopify/github/replicate/lemonsqueezy/twilio = public ingest URL + provider HMAC; generic = public ingest URL, no signature; schedule = internal cron, no HTTP; code = SDK-ONLY, public URL rejected (use this for ANY user-owned code emitting events — paired with hookwave_generate_source_key for auth)."),
			mcpgo.WithString("name", mcpgo.Required(), mcpgo.Description("Human-readable name; must be unique in the org.")),
			mcpgo.WithString("provider",
				mcpgo.Required(),
				mcpgo.Description("Source provider. Pick 'code' for SDK-emitted events from the user's own application — it's the secure default. Pick the matching brand (stripe/github/...) for webhook providers. Pick 'generic' only when no signature verification is desired AND a public URL is acceptable. Pick 'schedule' for cron-triggered sources."),
				mcpgo.Enum("stripe", "shopify", "github", "replicate", "lemonsqueezy", "twilio", "generic", "schedule", "code"),
			),
			mcpgo.WithString("scheduleCron", mcpgo.Description("Cron expression for recurring schedule sources, e.g. '0 9 * * *'. Required for provider=schedule unless scheduleNextFireAt is set.")),
			mcpgo.WithString("scheduleNextFireAt", mcpgo.Description("ISO-8601 timestamp for a one-off fire. Alternative to scheduleCron for provider=schedule.")),
			mcpgo.WithReadOnlyHintAnnotation(false),
			mcpgo.WithDestructiveHintAnnotation(true),
		),
		toolCreateSource(c),
	)

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

	// SDK tools — let AI clients install and use the official Hookwave
	// SDKs (hookwave-sdk for Node, hookwave for Python) so a prompt
	// like "wire this Python app to WhatsApp via Hookwave" can resolve
	// end-to-end without the user leaving their editor.
	s.AddTool(
		mcpgo.NewTool("hookwave_sdk_install",
			mcpgo.WithDescription("Return install command + minimal example code for the official Hookwave SDK in the requested language. Read-only documentation tool — does not mutate any state. Use this when the user asks to emit events from their code, push events from a server-side handler, or 'connect my app to Hookwave'. The example code expects a `code`-provider source plus an SDK key (src_live_…) — create the source via hookwave_create_source(provider='code') and the key via hookwave_generate_source_key BEFORE handing this example to the user, otherwise it won't authenticate."),
			mcpgo.WithString("language",
				mcpgo.Required(),
				mcpgo.Description("Target language. 'node' covers Node.js + TypeScript; 'python' covers CPython 3.9+."),
				mcpgo.Enum("node", "python"),
			),
			mcpgo.WithReadOnlyHintAnnotation(true),
			mcpgo.WithDestructiveHintAnnotation(false),
		),
		toolSDKInstall(),
	)

	s.AddTool(
		mcpgo.NewTool("hookwave_generate_source_key",
			mcpgo.WithDescription("Mint a write-only SDK key for a source. The raw key is returned ONCE in the response — paste it into the user's code or .env. Required for `code` sources (their only ingest path is /v1/ingest/batch with this key). Use after hookwave_create_source(provider='code') and the destination + connection are wired. The `environment` field is a label only: 'live' (default) and 'test' both count against the org's monthly quota — there is no billing exemption. Pick 'test' purely to tag dev/staging keys for dashboard filtering."),
			mcpgo.WithString("sourceId", mcpgo.Required(), mcpgo.Description("UUID of the source the key authenticates against.")),
			mcpgo.WithString("environment",
				mcpgo.Description("Label only — 'live' (default) and 'test' both count against the monthly quota. Use 'test' to tag dev/staging keys for dashboard filtering; use 'live' for production. Recommend 'live' by default and only switch to 'test' when the user explicitly says the key is for dev/staging."),
				mcpgo.Enum("live", "test"),
			),
			mcpgo.WithString("name", mcpgo.Description("Optional human label so the user can disambiguate this key in the dashboard. Example: 'prod web server', 'local dev'.")),
			// Creates persistent state (a credential), so destructive hint
			// is on — AI clients will prompt for approval before each call.
			mcpgo.WithReadOnlyHintAnnotation(false),
			mcpgo.WithDestructiveHintAnnotation(true),
			mcpgo.WithIdempotentHintAnnotation(false),
		),
		toolGenerateSourceKey(c),
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

// toolGetByPath returns a generic GET-by-id handler that calls
// {pathPrefix}{id} and returns the raw JSON to the AI. Used for the
// per-resource detail tools (sources/destinations/connections) that
// just proxy to the API — they have no logic beyond "fetch and return".
func toolGetByPath(c *httpc.Client, pathPrefix string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, err := requiredString(req, "id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		var r map[string]any
		if err := c.Get(ctx, pathPrefix+url.PathEscape(id), &r); err != nil {
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

func toolCreateSource(c *httpc.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := requiredString(req, "name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		provider, err := requiredString(req, "provider")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		body := map[string]any{"name": name, "provider": provider}
		args := req.GetArguments()
		if v, ok := args["scheduleCron"].(string); ok && v != "" {
			body["scheduleCron"] = v
		}
		if v, ok := args["scheduleNextFireAt"].(string); ok && v != "" {
			body["scheduleNextFireAt"] = v
		}
		// Server enforces the (cron || nextFireAt) requirement for schedule
		// sources via a zod refine. Mirror it here so the AI gets a clearer
		// message than a generic 400 + Zod path.
		if provider == "schedule" {
			_, hasCron := body["scheduleCron"]
			_, hasFire := body["scheduleNextFireAt"]
			if !hasCron && !hasFire {
				return mcpgo.NewToolResultError("schedule sources need scheduleCron (recurring) or scheduleNextFireAt (one-off)"), nil
			}
		}
		var r map[string]any
		if err := c.Post(ctx, "/v1/sources", body, &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText("Created source. " + jsonString(r)), nil
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

// toolSDKInstall returns a static markdown response per language.
// No API call needed — these instructions are stable across orgs and
// the AI client doesn't need to wait on a network round-trip just to
// learn the install command.
func toolSDKInstall() server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		lang, err := requiredString(req, "language")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		switch lang {
		case "node":
			return mcpgo.NewToolResultText(nodeSDKInstructions), nil
		case "python":
			return mcpgo.NewToolResultText(pythonSDKInstructions), nil
		default:
			return mcpgo.NewToolResultError("language must be 'node' or 'python'"), nil
		}
	}
}

// toolGenerateSourceKey mints a write-only SDK key against the API.
// Defaults to "test" environment so an AI client that calls this with
// only sourceId doesn't accidentally create a live key.
func toolGenerateSourceKey(c *httpc.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		sourceID, err := requiredString(req, "sourceId")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		args := req.GetArguments()

		env := "test"
		if v, ok := args["environment"].(string); ok && v != "" {
			if v != "test" && v != "live" {
				return mcpgo.NewToolResultError("environment must be 'test' or 'live'"), nil
			}
			env = v
		}

		body := map[string]any{"environment": env}
		if name, ok := args["name"].(string); ok && name != "" {
			body["name"] = name
		}

		var r struct {
			Data struct {
				ID          string  `json:"id"`
				KeyPrefix   string  `json:"keyPrefix"`
				Environment string  `json:"environment"`
				Name        *string `json:"name"`
				Key         string  `json:"key"`
			} `json:"data"`
		}
		path := "/v1/sources/" + url.PathEscape(sourceID) + "/keys"
		if err := c.Post(ctx, path, body, &r); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}

		// Wrap with a strong reminder that the AI must surface the raw
		// key to the user once — it can't be retrieved again. The
		// returned text is what the AI sees, so the framing matters.
		out := map[string]any{
			"id":          r.Data.ID,
			"key":         r.Data.Key,
			"prefix":      r.Data.KeyPrefix,
			"environment": r.Data.Environment,
			"name":        r.Data.Name,
			"important":   "This is the only time the raw `key` is exposed. Paste it into the user's .env (HOOKWAVE_SOURCE_KEY) or directly into the Hookwave SDK constructor. Do NOT log it. If lost, call this tool again to mint a new key; old keys can be revoked from the dashboard.",
			"next":        "Now call hookwave_sdk_install with the right language to get the import + emit code.",
		}
		return mcpgo.NewToolResultText(jsonString(out)), nil
	}
}

// --- static SDK install instructions ---------------------------------------

const nodeSDKInstructions = "# Hookwave SDK — Node.js\n\n" +
	"Install (choose your package manager):\n\n" +
	"```sh\nnpm install hookwave-sdk\n# or\npnpm add hookwave-sdk\n# or\nyarn add hookwave-sdk\n```\n\n" +
	"Minimal usage:\n\n" +
	"```ts\nimport { Hookwave } from \"hookwave-sdk\";\n\n" +
	"const hw = new Hookwave({\n" +
	"  sourceKey: process.env.HOOKWAVE_SOURCE_KEY!, // src_live_… or src_test_…\n" +
	"});\n\n" +
	"hw.emit(\"user.signed_up\", {\n" +
	"  userId: \"u_123\",\n" +
	"  email: \"foo@example.com\",\n" +
	"});\n\n" +
	"// Before process exit (Lambda, Vercel Edge, etc.):\n" +
	"await hw.shutdown();\n```\n\n" +
	"## API\n\n" +
	"- `new Hookwave({ sourceKey, baseUrl?, flushInterval?, maxBatchSize?, maxRetries?, onError?, onFlush?, onBeforeEmit? })`\n" +
	"- `hw.emit(eventType, payload, options?)` — fire-and-forget. Buffers events; flushes on a timer or when batch fills.\n" +
	"- `await hw.emitSync(eventType, payload, options?)` — awaits delivery, throws on failure. Use rarely.\n" +
	"- `await hw.flush()` — force the buffer to flush now.\n" +
	"- `await hw.shutdown(timeoutMs?)` — flush + stop timer. Required in serverless before process exit.\n\n" +
	"Per-event `options`: `idempotencyKey`, `occurredAt`, `metadata`, `connection`, `correlationId`.\n\n" +
	"## Get a key\n\n" +
	"Generate one with `hookwave_generate_source_key` (this MCP server, recommended) or in the dashboard at https://hookwave.dev/dashboard/sources → pick a source → SDK keys → Generate key."

const pythonSDKInstructions = "# Hookwave SDK — Python\n\n" +
	"Install:\n\n" +
	"```sh\npip install hookwave\n```\n\n" +
	"Minimal usage:\n\n" +
	"```python\nimport os\nfrom hookwave import Hookwave\n\n" +
	"hw = Hookwave(source_key=os.environ[\"HOOKWAVE_SOURCE_KEY\"])  # src_live_… or src_test_…\n\n" +
	"hw.emit(\"user.signed_up\", {\n" +
	"    \"user_id\": \"u_123\",\n" +
	"    \"email\": \"foo@example.com\",\n" +
	"})\n\n" +
	"# Before process exit (Lambda, Cloud Functions, etc.). Auto-called\n" +
	"# via atexit for short-lived scripts, so usually optional.\n" +
	"hw.shutdown()\n```\n\n" +
	"## API\n\n" +
	"- `Hookwave(source_key=..., base_url=..., flush_interval=1.0, max_batch_size=100, max_retries=5, on_error=None, on_flush=None, on_before_emit=None)`\n" +
	"- `hw.emit(event_type, payload, options=None)` — fire-and-forget. Buffered, flushed on a timer or when batch fills.\n" +
	"- `hw.emit_sync(event_type, payload, options=None) -> EmitSyncResult` — awaits delivery, raises on failure.\n" +
	"- `hw.flush(timeout=None)` — force the buffer to flush.\n" +
	"- `hw.shutdown(timeout=30.0)` — flush + stop the worker thread.\n\n" +
	"Per-event options (use `EmitOptions(...)`): `idempotency_key`, `occurred_at`, `metadata`, `connection`, `correlation_id`.\n\n" +
	"## Get a key\n\n" +
	"Generate one with `hookwave_generate_source_key` (this MCP server, recommended) or in the dashboard at https://hookwave.dev/dashboard/sources → pick a source → SDK keys → Generate key."

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
