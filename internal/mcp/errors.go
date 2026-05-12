package mcp

import (
	"encoding/json"
	"errors"
	"strings"

	qbt "github.com/autobrr/go-qbittorrent"
)

// ErrCode is a stable string identifier the agent can branch on.
type ErrCode string

const (
	CodeInvalidArgument     ErrCode = "invalid_argument"
	CodeUpstreamUnavailable ErrCode = "upstream_unavailable"
	// CodeUpstreamForbidden signals the loopback-auth-bypass assumption was
	// wrong. qBittorrent returned 401/403 (or autobrr's ErrBadCredentials /
	// ErrIPBanned bubbled up): the operator must enable "Bypass authentication
	// for clients on localhost" in the WebUI settings.
	CodeUpstreamForbidden ErrCode = "upstream_forbidden"
	CodeUpstreamNotFound  ErrCode = "upstream_not_found"
	CodeInternal          ErrCode = "internal"
)

// ToolError is the structured payload returned to MCP clients on tool failure.
// It implements error so it can flow through Go error returns.
type ToolError struct {
	Code      ErrCode `json:"code"`
	Message   string  `json:"message"`
	Retriable bool    `json:"retriable"`
}

func (e *ToolError) Error() string { return string(e.Code) + ": " + e.Message }

// JSON renders the ToolError as a single-line JSON object suitable for
// embedding in a TextContent body.
func (e *ToolError) JSON() string {
	b, err := json.Marshal(e)
	if err != nil {
		return `{"code":"internal","message":"failed to marshal error","retriable":false}`
	}
	return string(b)
}

// errorFromSDK translates autobrr/go-qbittorrent SDK errors into *ToolError
// with appropriate codes. Use from any handler that calls into qbt.Client.
//
// The SDK wraps non-2xx responses as qbt.ErrUnexpectedStatus without
// preserving the numeric status code on the typed error, so 401 / 403
// detection falls back to a string-match heuristic against the wrapped
// message. Brittle if the SDK changes its error format upstream, but the
// alternative is forking; revisit when autobrr exposes the status code
// programmatically.
func errorFromSDK(err error) *ToolError {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case errors.Is(err, qbt.ErrBadCredentials),
		errors.Is(err, qbt.ErrIPBanned),
		strings.Contains(msg, "status code: 401"),
		strings.Contains(msg, "status code: 403"),
		// autobrr retries the request after a 401/403 by attempting a
		// re-login. Each retry fails the same way and the final wrapped
		// error contains "qbit re-login". This is the reliable surface for
		// auth failure detection — the original 4xx status is not preserved
		// on the typed error.
		strings.Contains(msg, "qbit re-login"):
		return &ToolError{
			Code:      CodeUpstreamForbidden,
			Message:   msg,
			Retriable: false,
		}
	case errors.Is(err, qbt.ErrTorrentNotFound),
		errors.Is(err, qbt.ErrRSSItemNotFound),
		errors.Is(err, qbt.ErrRSSRuleNotFound):
		return &ToolError{
			Code:      CodeUpstreamNotFound,
			Message:   msg,
			Retriable: false,
		}
	case errors.Is(err, qbt.ErrInvalidTorrentHash),
		errors.Is(err, qbt.ErrEmptyTorrentName),
		errors.Is(err, qbt.ErrInvalidPriority),
		errors.Is(err, qbt.ErrInvalidURL),
		errors.Is(err, qbt.ErrRSSPathConflict):
		return &ToolError{
			Code:      CodeInvalidArgument,
			Message:   msg,
			Retriable: false,
		}
	default:
		return &ToolError{
			Code:      CodeUpstreamUnavailable,
			Message:   msg,
			Retriable: true,
		}
	}
}
