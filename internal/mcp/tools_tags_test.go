package mcp

import (
	"context"
	"net/http"
	"testing"

	qbt "github.com/autobrr/go-qbittorrent"
)

func callListTags(t *testing.T, client *qbt.Client) (ListTagsOutput, *ToolError) {
	t.Helper()
	h := listTagsHandler(client)
	return h(context.Background(), ListTagsInput{})
}

func TestListTags_ReturnsArray(t *testing.T) {
	client, captured := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/tags": {body: `["weekly","complete","tvdb:12345"]`},
	})
	out, terr := callListTags(t, client)
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if len(out.Tags) != 3 {
		t.Errorf("tags = %v, want 3 entries", out.Tags)
	}
	if captured["/api/v2/torrents/tags"].Method != http.MethodGet {
		t.Errorf("method = %q, want GET", captured["/api/v2/torrents/tags"].Method)
	}
}

func TestListTags_EmptyArray(t *testing.T) {
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/tags": {body: `[]`},
	})
	out, terr := callListTags(t, client)
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if out.Tags == nil {
		t.Error("Tags should be non-nil empty slice (not nil), for stable JSON output")
	}
	if len(out.Tags) != 0 {
		t.Errorf("tags = %v, want empty", out.Tags)
	}
}

func TestListTags_NullArrayNormalizedToEmpty(t *testing.T) {
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/tags": {body: `null`},
	})
	out, terr := callListTags(t, client)
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if out.Tags == nil {
		t.Error("null upstream should be normalized to empty slice, not nil")
	}
}

func TestListTags_Upstream500(t *testing.T) {
	client := newQbitMockStatus(t, http.StatusInternalServerError)
	_, terr := callListTags(t, client)
	if terr == nil || terr.Code != CodeUpstreamUnavailable {
		t.Errorf("err = %+v, want upstream_unavailable", terr)
	}
}

func TestListTags_Upstream403(t *testing.T) {
	client := newQbitMockStatus(t, http.StatusForbidden)
	_, terr := callListTags(t, client)
	if terr == nil || terr.Code != CodeUpstreamForbidden {
		t.Errorf("err = %+v, want upstream_forbidden", terr)
	}
}
