package mcp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	qbt "github.com/autobrr/go-qbittorrent"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wyvernzora/qbittorrent-mcp/internal/savepath"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startTestSession spins up an in-memory MCP server backed by an autobrr
// qBittorrent client pointed at an unreachable host (no tools call it yet)
// and returns a connected client session.
func startTestSession(t *testing.T) (*mcpsdk.ClientSession, func()) { //nolint:gocritic
	t.Helper()
	client := qbt.NewClient(qbt.Config{
		Host:    "http://127.0.0.1:1", // unreachable; no tools call it yet
		Timeout: 1,                    // seconds; autobrr-config field is int
	})
	resolver, err := savepath.Parse("")
	if err != nil {
		t.Fatalf("savepath.Parse: %v", err)
	}
	server := New(client, resolver, discardLogger(), "test")

	t1, t2 := mcpsdk.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.0"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	cleanup := func() {
		_ = cs.Close()
		cancel()
	}
	return cs, cleanup
}

func TestListTools_All11Registered(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		// downloads (3)
		"list_downloads":   false,
		"add_download":     false,
		"remove_downloads": false,
		// tags (1)
		"list_tags": false,
		// destinations (1)
		"list_destinations": false,
		// rss (6)
		"list_rss":        false,
		"add_rss_feed":    false,
		"remove_rss_item": false,
		"list_rss_rules":  false,
		"set_rss_rule":    false,
		"delete_rss_rule": false,
	}
	for _, tool := range res.Tools {
		if _, ok := want[tool.Name]; !ok {
			t.Errorf("unexpected tool registered: %s", tool.Name)
			continue
		}
		want[tool.Name] = true
		if tool.InputSchema == nil {
			t.Errorf("%s: nil InputSchema", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s: empty Description", tool.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing tool: %s", name)
		}
	}
	if len(res.Tools) != len(want) {
		t.Errorf("tool count = %d, want %d", len(res.Tools), len(want))
	}
}

func TestCallTool_StubReturnsNotImplemented(t *testing.T) {
	cs, cleanup := startTestSession(t)
	defer cleanup()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "list_downloads",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected stub handler to return IsError=true")
	}
}

func TestHTTPTransport_Healthz(t *testing.T) {
	client := qbt.NewClient(qbt.Config{Host: "http://127.0.0.1:1", Timeout: 1})
	resolver, _ := savepath.Parse("")
	server := New(client, resolver, discardLogger(), "test")

	mux := http.NewServeMux()
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	hs := httptest.NewServer(mux)
	defer hs.Close()

	resp, err := http.Get(hs.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "ok" {
		t.Errorf("body = %q", string(b))
	}
}
