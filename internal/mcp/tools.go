package mcp

import (
	"context"
	"log/slog"
	"time"

	qbt "github.com/autobrr/go-qbittorrent"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// internalHandler is the shape every tool implements internally. The caller
// (wrap) is responsible for translating *ToolError into the SDK's error
// result and recording the err code for logs without round-tripping JSON.
type internalHandler[I, O any] func(ctx context.Context, in I) (O, *ToolError)

// Register adds all qBittorrent tools to the given server.
//
// No tools are registered yet — this is bootstrap scaffolding. Add the first
// tool by:
//
//  1. Defining input/output structs with `json` + `jsonschema` tags.
//  2. Writing a handler of type `internalHandler[I, O]` that calls into
//     the qBittorrent client (github.com/autobrr/go-qbittorrent).
//  3. Calling `mcpsdk.AddTool(s, &mcpsdk.Tool{Name, Description, Annotations},
//     wrap("tool_name", logger, handler))`.
//
// See ../../../dmhy-mcp/internal/mcp/tools.go for a worked example of the
// same pattern.
func Register(_ *mcpsdk.Server, _ *qbt.Client, _ *slog.Logger) {
	// register tools here
}

// wrap adapts an internalHandler into the SDK signature. It records logging
// at info level (without leaking the raw input) and produces a structured
// CallToolResult on error.
//
//nolint:unused // exported via type only until the first tool is registered
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
//
//nolint:unused // helper reserved for the first tool's error path
func errorResult(te *ToolError) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: te.JSON()},
		},
	}
}
