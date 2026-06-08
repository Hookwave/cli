package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hookwave/cli/internal/output"
)

// eventRow mirrors the API list response — additive so `events list --json`
// doesn't silently drop fields the API returns. The table renderer in
// `list` only reads the leading fields; downstream JSON consumers get
// the full per-event picture (latency, retry schedule, fan-out targets).
type eventRow struct {
	ID                 string         `json:"id"`
	Status             string         `json:"status"`
	Verified           bool           `json:"verified"`
	SourceID           string         `json:"sourceId"`
	SourceName         string         `json:"sourceName"`
	Provider           *string        `json:"provider,omitempty"`
	ReceivedAt         time.Time      `json:"receivedAt"`
	LastResponseStatus *int           `json:"lastResponseStatus,omitempty"`
	LastDurationMs     *int           `json:"lastDurationMs,omitempty"`
	NextAttemptAt      *time.Time     `json:"nextAttemptAt,omitempty"`
	Connections        []eventConnRow `json:"connections,omitempty"`
}

type eventConnRow struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
}

type eventsListResp struct {
	Data       []eventRow `json:"data"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

type eventDetailResp struct {
	Data map[string]any `json:"data"`
}

type replayResp struct {
	Data struct {
		Replayed int      `json:"replayed"`
		IDs      []string `json:"ids"`
	} `json:"data"`
}

func newEventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Inspect and replay events",
	}
	cmd.AddCommand(
		newEventsListCmd(),
		newEventsGetCmd(),
		newEventsReplayCmd(),
		newEventsExportCmd(),
	)
	return cmd
}

func newEventsListCmd() *cobra.Command {
	var (
		limit    int
		status   string
		sourceID string
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent events",
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", fmt.Sprintf("%d", limit))
			}
			if status != "" {
				q.Set("status", status)
			}
			if sourceID != "" {
				// API events route uses snake_case; the CLI flag is
				// human-friendly camelCase. Previously this was silently
				// dropped server-side and returned unfiltered events.
				q.Set("source_id", sourceID)
			}
			path := "/v1/events"
			if s := q.Encode(); s != "" {
				path += "?" + s
			}
			var r eventsListResp
			if err := c.Get(cmd.Context(), path, &r); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(a.stdout, r.Data)
			}
			return renderEventsTable(a.stdout, r.Data)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 25, "max rows to fetch")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (queued|delivering|delivered|failed)")
	cmd.Flags().StringVar(&sourceID, "source", "", "filter by source id")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a table")
	return cmd
}

func newEventsGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <event-id>",
		Short: "Print the full JSON for a single event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			var r eventDetailResp
			if err := c.Get(cmd.Context(), "/v1/events/"+url.PathEscape(args[0]), &r); err != nil {
				return err
			}
			return printJSON(a.stdout, r.Data)
		},
	}
	return cmd
}

func newEventsReplayCmd() *cobra.Command {
	var edit bool
	cmd := &cobra.Command{
		Use:   "replay <event-id>...",
		Short: "Re-queue one or more events for delivery",
		Long: `Re-queues events for delivery without re-triggering the original source.

  hookwave events replay evt_abc                # bulk replay as-is
  hookwave events replay evt_abc evt_def
  hookwave events replay evt_abc --edit         # open $EDITOR to modify the body, then replay`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			if edit {
				if len(args) != 1 {
					return fmt.Errorf("--edit only supports one event at a time")
				}
				return runReplayWithEdit(cmd.Context(), a, c, args[0])
			}
			// API expects snake_case event_ids; this was previously sent
			// as eventIds and silently stripped by Zod, leading to a
			// 200 response with zero events actually re-queued.
			body := map[string]any{"event_ids": args}
			var r replayResp
			if err := c.Post(cmd.Context(), "/v1/events/replay", body, &r); err != nil {
				return err
			}
			a.stdout.Printf(output.Success, "Replayed %d event(s)\n", r.Data.Replayed)
			return nil
		},
	}
	cmd.Flags().BoolVar(&edit, "edit", false, "open the body in $EDITOR before replaying")
	return cmd
}

func renderEventsTable(p *output.Printer, rows []eventRow) error {
	if len(rows) == 0 {
		p.Println(output.Muted, "(no events)")
		return nil
	}
	tw := tabwriter.NewWriter(&stdoutWriter{p}, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tSOURCE\tRECEIVED")
	for _, r := range rows {
		status := r.Status
		switch r.Status {
		case "delivered":
			status = p.Stylize(output.Success, status)
		case "failed":
			status = p.Stylize(output.Error, status)
		case "queued", "delivering":
			status = p.Stylize(output.Warn, status)
		}
		src := r.SourceName
		if src == "" {
			src = r.SourceID
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			truncate(r.ID, 16),
			status,
			truncate(src, 28),
			r.ReceivedAt.Local().Format("2006-01-02 15:04:05"),
		)
	}
	return tw.Flush()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func printJSON(p *output.Printer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	p.Plain(string(b))
	if !strings.HasSuffix(string(b), "\n") {
		p.Plain("\n")
	}
	return nil
}

// `hookwave events export` — streams CSV / JSON to a file or stdout.
// Server caps at 50k rows per request and feature-gates to Pro tier;
// over-quota orgs see a 402 with an "Upgrade" message.
func newEventsExportCmd() *cobra.Command {
	var (
		format       string
		status       string
		sourceID     string
		connectionID string
		fromTime     string
		toTime       string
		limit        int
		outputPath   string
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Stream events as CSV or JSON (Pro feature)",
		Long: `Bulk-export events matching a filter as CSV (default) or JSON.

  hookwave events export --status failed -o failures.csv
  hookwave events export --format json --from 2026-04-01T00:00:00Z > events.jsonl
  hookwave events export --source <id> --limit 10000 -o source-events.csv

The server caps at 50,000 rows per request and feature-gates this to
Pro orgs.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			if format != "csv" && format != "json" {
				return fmt.Errorf("--format must be csv or json, got %q", format)
			}
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			q := url.Values{}
			q.Set("format", format)
			if status != "" {
				q.Set("status", status)
			}
			if sourceID != "" {
				q.Set("source_id", sourceID)
			}
			if connectionID != "" {
				q.Set("connection_id", connectionID)
			}
			if fromTime != "" {
				q.Set("from", fromTime)
			}
			if toTime != "" {
				q.Set("to", toTime)
			}
			if limit > 0 {
				q.Set("limit", fmt.Sprintf("%d", limit))
			}
			path := "/v1/events/export?" + q.Encode()

			// Stream the response. We can't use httpc.Client.Get here
			// because the response body isn't JSON — it's the export
			// payload. Build the request manually and pipe the body.
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, c.Base()+path, nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+c.Token())
			req.Header.Set("Accept", "*/*")
			hc := &http.Client{Timeout: 60 * time.Second}
			resp, err := hc.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
				return fmt.Errorf("export failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}

			var sink io.Writer
			if outputPath == "" || outputPath == "-" {
				sink = os.Stdout
			} else {
				f, ferr := os.Create(outputPath)
				if ferr != nil {
					return ferr
				}
				defer f.Close()
				sink = f
			}
			n, err := io.Copy(sink, resp.Body)
			if err != nil {
				return err
			}
			if outputPath != "" && outputPath != "-" {
				a.stdout.Printf(output.Success, "✓ Exported %d bytes → %s\n", n, outputPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "csv", "csv or json (jsonl for json mode — one event per line)")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (queued|delivering|delivered|failed|dropped)")
	cmd.Flags().StringVar(&sourceID, "source", "", "filter by source UUID")
	cmd.Flags().StringVar(&connectionID, "connection", "", "filter by connection UUID")
	cmd.Flags().StringVar(&fromTime, "from", "", "ISO timestamp (inclusive lower bound)")
	cmd.Flags().StringVar(&toTime, "to", "", "ISO timestamp (exclusive upper bound)")
	cmd.Flags().IntVar(&limit, "limit", 0, "max rows (default 50000, max 50000)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "write to this file; '-' or omit for stdout")
	return cmd
}

// suppress unused-import gripes when --output writes to stdout only.
var _ = errors.New
var _ = context.Canceled
