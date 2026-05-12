package mcp

import (
	"context"
	"log/slog"
	"time"

	qbt "github.com/autobrr/go-qbittorrent"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wyvernzora/qbittorrent-mcp/internal/savepath"
)

// internalHandler is the shape every tool implements internally. The caller
// (wrap) is responsible for translating *ToolError into the SDK's error
// result and recording the err code for logs without round-tripping JSON.
type internalHandler[I, O any] func(ctx context.Context, in I) (O, *ToolError)

// Register adds all qBittorrent tools to the given server. The tool surface
// is split by domain — see internal/mcp/tools_downloads.go,
// internal/mcp/tools_tags.go, internal/mcp/tools_destinations.go, and
// internal/mcp/tools_rss.go for the per-domain registrations.
// docs/tools.md is the design spec for the whole surface.
//
// resolver is the deploy-time destination alias map; tool handlers that
// accept a `destination` input translate the alias name into the upstream
// save_path via resolver.Resolve.
func Register(s *mcpsdk.Server, client *qbt.Client, resolver *savepath.Resolver, logger *slog.Logger) {
	registerDownloads(s, client, resolver, logger)
	registerTags(s, client, resolver, logger)
	registerDestinations(s, client, resolver, logger)
	registerRSS(s, client, resolver, logger)
}

// wrap adapts an internalHandler into the SDK signature. It records logging
// at info level (without leaking the raw input) and produces a structured
// CallToolResult on error.
func wrap[I, O any](name string, logger *slog.Logger, h internalHandler[I, O]) mcpsdk.ToolHandlerFor[I, O] {
	return func(ctx context.Context, _ *mcpsdk.CallToolRequest, in I) (*mcpsdk.CallToolResult, O, error) {
		start := time.Now()
		logger.Debug("tool call start", "tool", name, "input", in)
		out, terr := h(ctx, in)
		dur := time.Since(start)
		errCode := ""
		if terr != nil {
			errCode = string(terr.Code)
		}
		logger.Info("tool call",
			"tool", name,
			"duration_ms", dur.Milliseconds(),
			"err_code", errCode,
		)
		if terr != nil {
			return errorResult(terr), out, nil
		}
		return nil, out, nil
	}
}

// errorResult builds a CallToolResult carrying the structured ToolError JSON
// so MCP clients can branch on `code` programmatically.
func errorResult(te *ToolError) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: te.JSON()},
		},
	}
}

// readOnlyAnnotations is the ToolAnnotations preset used by every read-only
// tool (list_*, get_*). qBittorrent state is external to the MCP server so
// OpenWorldHint is always true; the read tools are idempotent and never
// mutate.
//
//nolint:unused // referenced by the per-domain registrars in tools_*.go
func readOnlyAnnotations() *mcpsdk.ToolAnnotations {
	yes, no := true, false
	return &mcpsdk.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: &no,
		IdempotentHint:  true,
		OpenWorldHint:   &yes,
	}
}

// mutatingAnnotations is the preset for tools that mutate qBittorrent state
// (add_*, remove_*, set_rss_rule, …). DestructiveHint is true only on the
// actually-destructive ops (remove_*); the rest are non-destructive
// mutations.
//
//nolint:unused // referenced by the per-domain registrars in tools_*.go
func mutatingAnnotations(destructive bool) *mcpsdk.ToolAnnotations {
	yes := true
	d := destructive
	return &mcpsdk.ToolAnnotations{
		ReadOnlyHint:    false,
		DestructiveHint: &d,
		IdempotentHint:  false,
		OpenWorldHint:   &yes,
	}
}

// auditMutation emits a structured slog record for an agent-initiated
// mutation so operators can investigate later. The "audit=true" field is
// the grep-anchor; "action" is a short verb (add, remove). level
// differentiates severity — remove fires at warn so log aggregators
// filtering on level surface it more prominently, while reversible ops
// sit at info.
//
// The call site fires *before* the upstream SDK call so the record reflects
// agent intent even when upstream rejects the request. wrap's existing
// timing log captures the upstream outcome separately.
func auditMutation(ctx context.Context, logger *slog.Logger, level slog.Level, action string, hashes []string, extra ...slog.Attr) {
	attrs := []slog.Attr{
		slog.Bool("audit", true),
		slog.String("action", action),
		slog.Any("hashes", hashes),
		slog.Int("count", len(hashes)),
	}
	attrs = append(attrs, extra...)
	logger.LogAttrs(ctx, level, "tool audit", attrs...)
}
