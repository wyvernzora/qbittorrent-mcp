package mcp

import (
	"context"
	"testing"

	"github.com/wyvernzora/qbittorrent-mcp/internal/savepath"
)

func callListDestinations(t *testing.T, resolver *savepath.Resolver) (ListDestinationsOutput, *ToolError) {
	t.Helper()
	h := listDestinationsHandler(resolver)
	return h(context.Background(), ListDestinationsInput{})
}

func TestListDestinations_Empty(t *testing.T) {
	resolver, err := savepath.Parse("")
	if err != nil {
		t.Fatalf("savepath.Parse: %v", err)
	}
	out, terr := callListDestinations(t, resolver)
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if out.Destinations == nil {
		t.Error("Destinations should be non-nil empty slice (not nil), for stable JSON output")
	}
	if len(out.Destinations) != 0 {
		t.Errorf("destinations = %v, want empty", out.Destinations)
	}
}

func TestListDestinations_Populated(t *testing.T) {
	resolver, err := savepath.Parse("kura-inbox=/srv/kura,general=/srv/general")
	if err != nil {
		t.Fatalf("savepath.Parse: %v", err)
	}
	out, terr := callListDestinations(t, resolver)
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if len(out.Destinations) != 2 {
		t.Fatalf("destinations = %v, want 2 entries", out.Destinations)
	}
	// Resolver.Names() is sorted alphabetically.
	if out.Destinations[0].Name != "general" || out.Destinations[0].Path != "/srv/general" {
		t.Errorf("entry[0] = %+v, want {general, /srv/general}", out.Destinations[0])
	}
	if out.Destinations[1].Name != "kura-inbox" || out.Destinations[1].Path != "/srv/kura" {
		t.Errorf("entry[1] = %+v, want {kura-inbox, /srv/kura}", out.Destinations[1])
	}
}
