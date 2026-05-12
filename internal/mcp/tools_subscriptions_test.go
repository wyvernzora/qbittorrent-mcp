package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	qbt "github.com/autobrr/go-qbittorrent"
)

// --- helpers ---

func feedHashForTest(t *testing.T, url string) string {
	t.Helper()
	p := feedPathForURL(url)
	if !strings.HasPrefix(p, feedPathPrefix) {
		t.Fatalf("feedPath = %q, want prefix %q", p, feedPathPrefix)
	}
	return p
}

const testFeedURL = "https://example.com/feed.xml"

// rssItemsBody returns a JSON body for /api/v2/rss/items where each
// path in feedPaths is registered as a feed pointing at feedURL.
// Articles are inlined per the qBittorrent shape so the withData=true
// path exercises article projection.
func rssItemsBody(feedPath, feedURL string, articles []map[string]string) string {
	var arts []string
	for _, a := range articles {
		fields := make([]string, 0, len(a))
		for k, v := range a {
			fields = append(fields, fmt.Sprintf("%q:%q", k, v))
		}
		arts = append(arts, "{"+strings.Join(fields, ",")+"}")
	}
	feed := fmt.Sprintf(`{"uid":"u","url":%q,"hasError":false,"articles":[%s]}`,
		feedURL, strings.Join(arts, ","),
	)
	return fmt.Sprintf("{%q:%s}", feedPath, feed)
}

// rssRulesBody returns the JSON body for /api/v2/rss/rules with a
// single managed rule whose AffectedFeeds points at feedPath.
func rssRulesBody(ruleName, feedPath, savePath, lastMatch string, tags []string) string {
	tagsJSON := "[]"
	if len(tags) > 0 {
		tagsJSON = `["` + strings.Join(tags, `","`) + `"]`
	}
	return fmt.Sprintf(`{%q:{"enabled":true,"useRegex":false,"mustContain":"","mustNotContain":"","affectedFeeds":[%q],"lastMatch":%q,"ignoreDays":0,"smartFilter":false,"torrentParams":{"tags":%s,"save_path":%q}}}`,
		ruleName, feedPath, lastMatch, tagsJSON, savePath,
	)
}

func callListSubscriptions(t *testing.T, client *qbt.Client, in ListSubscriptionsInput) (ListSubscriptionsOutput, *ToolError) {
	t.Helper()
	h := listSubscriptionsHandler(client, mustResolver(t, "kura-inbox=/mnt/kura"))
	return h(context.Background(), in)
}

func callSetSubscription(t *testing.T, client *qbt.Client, in SetSubscriptionInput) (OkOutput, *ToolError) {
	t.Helper()
	h := setSubscriptionHandler(client, mustResolver(t, "kura-inbox=/mnt/kura"), discardLogger())
	return h(context.Background(), in)
}

func callDeleteSubscription(t *testing.T, client *qbt.Client, in DeleteSubscriptionInput) (OkOutput, *ToolError) {
	t.Helper()
	h := deleteSubscriptionHandler(client, discardLogger())
	return h(context.Background(), in)
}

// --- feedPathForURL ---

func TestFeedPathForURL_DeterministicAndFlat(t *testing.T) {
	got := feedPathForURL(testFeedURL)
	if !strings.HasPrefix(got, feedPathPrefix) {
		t.Errorf("feedPath = %q, want prefix %q", got, feedPathPrefix)
	}
	if strings.ContainsAny(got, `/\`) {
		t.Errorf("feedPath = %q, want no folder separators (flat)", got)
	}
	hash := strings.TrimPrefix(got, feedPathPrefix)
	if len(hash) != 16 {
		t.Errorf("hash len = %d, want 16", len(hash))
	}
	if got2 := feedPathForURL(testFeedURL); got2 != got {
		t.Errorf("non-deterministic: %q vs %q", got, got2)
	}
}

func TestFeedPathForURL_DifferentURLsDifferentPaths(t *testing.T) {
	a := feedPathForURL(testFeedURL)
	b := feedPathForURL("https://example.com/other.xml")
	if a == b {
		t.Errorf("distinct URLs produced same path: %q", a)
	}
}

// --- list_subscriptions ---

func TestListSubscriptions_ManagedRuleProjected(t *testing.T) {
	feedPath := feedHashForTest(t, testFeedURL)
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules": {body: rssRulesBody("kura-show", feedPath, "/mnt/kura", "2026-05-10T18:24:00", []string{"tvdb:12345"})},
		"/api/v2/rss/items": {body: rssItemsBody(feedPath, testFeedURL, nil)},
	})
	out, terr := callListSubscriptions(t, client, ListSubscriptionsInput{})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if len(out.Subscriptions) != 1 {
		t.Fatalf("subscriptions = %d, want 1", len(out.Subscriptions))
	}
	s := out.Subscriptions[0]
	if s.Name != "kura-show" {
		t.Errorf("name = %q", s.Name)
	}
	if s.FeedURL != testFeedURL {
		t.Errorf("feed_url = %q", s.FeedURL)
	}
	if s.SavePath != "/mnt/kura" {
		t.Errorf("save_path = %q", s.SavePath)
	}
	if s.Destination != "kura-inbox" {
		t.Errorf("destination = %q, want reverse-resolved 'kura-inbox'", s.Destination)
	}
	if len(s.Tags) != 1 || s.Tags[0] != "tvdb:12345" {
		t.Errorf("tags = %v", s.Tags)
	}
	if s.LastMatchDate != "2026-05-10T18:24:00" {
		t.Errorf("last_match_date = %q", s.LastMatchDate)
	}
	if s.RecentItems != nil {
		t.Errorf("recent_items should be omitted when include_recent_items=false")
	}
}

func TestListSubscriptions_UnmanagedRulesFilteredOut(t *testing.T) {
	// One managed rule + one rule that targets a feed outside our prefix.
	// Only the managed one should surface.
	managedPath := feedHashForTest(t, testFeedURL)
	rules := fmt.Sprintf(
		`{"managed":{"enabled":true,"affectedFeeds":[%q],"torrentParams":{"tags":[]}},"other":{"enabled":true,"affectedFeeds":["Anime\\Erai-raws"],"torrentParams":{"tags":[]}}}`,
		managedPath,
	)
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules": {body: rules},
		"/api/v2/rss/items": {body: rssItemsBody(managedPath, testFeedURL, nil)},
	})
	out, terr := callListSubscriptions(t, client, ListSubscriptionsInput{})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if len(out.Subscriptions) != 1 || out.Subscriptions[0].Name != "managed" {
		t.Errorf("subscriptions = %+v, want only 'managed'", out.Subscriptions)
	}
}

func TestListSubscriptions_IncludeRecentItemsEmbedsArticles(t *testing.T) {
	feedPath := feedHashForTest(t, testFeedURL)
	articles := []map[string]string{
		{"id": "a1", "title": "Ep 03", "date": "2026-05-10T18:24:00Z", "torrentURL": "magnet:?xt=urn:btih:abc"},
		{"id": "a2", "title": "Ep 02", "date": "2026-05-03T18:24:00Z", "torrentURL": "magnet:?xt=urn:btih:def"},
	}
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules": {body: rssRulesBody("kura-show", feedPath, "/mnt/kura", "", nil)},
		"/api/v2/rss/items": {body: rssItemsBody(feedPath, testFeedURL, articles)},
	})
	out, terr := callListSubscriptions(t, client, ListSubscriptionsInput{IncludeRecentItems: true})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if len(out.Subscriptions) != 1 {
		t.Fatalf("subscriptions = %d, want 1", len(out.Subscriptions))
	}
	items := out.Subscriptions[0].RecentItems
	if len(items) != 2 {
		t.Fatalf("recent_items = %d, want 2", len(items))
	}
	if items[0].Link != "magnet:?xt=urn:btih:abc" {
		t.Errorf("link should prefer torrentURL; got %q", items[0].Link)
	}
}

func TestListSubscriptions_SinceFiltersOlderArticles(t *testing.T) {
	feedPath := feedHashForTest(t, testFeedURL)
	articles := []map[string]string{
		{"id": "new", "title": "new", "date": "2026-05-10T18:24:00Z", "torrentURL": "magnet:new"},
		{"id": "old", "title": "old", "date": "2026-04-01T00:00:00Z", "torrentURL": "magnet:old"},
	}
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules": {body: rssRulesBody("kura-show", feedPath, "", "", nil)},
		"/api/v2/rss/items": {body: rssItemsBody(feedPath, testFeedURL, articles)},
	})
	out, _ := callListSubscriptions(t, client, ListSubscriptionsInput{
		IncludeRecentItems: true,
		Since:              "2026-05-01T00:00:00Z",
	})
	items := out.Subscriptions[0].RecentItems
	if len(items) != 1 || items[0].Title != "new" {
		t.Errorf("since filter failed; items = %+v", items)
	}
}

func TestListSubscriptions_InvalidSinceRejected(t *testing.T) {
	feedPath := feedHashForTest(t, testFeedURL)
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules": {body: rssRulesBody("k", feedPath, "", "", nil)},
		"/api/v2/rss/items": {body: rssItemsBody(feedPath, testFeedURL, nil)},
	})
	_, terr := callListSubscriptions(t, client, ListSubscriptionsInput{Since: "not-a-date"})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v, want invalid_argument", terr)
	}
}

func TestListSubscriptions_Upstream500(t *testing.T) {
	client := newQbitMockStatus(t, http.StatusInternalServerError)
	_, terr := callListSubscriptions(t, client, ListSubscriptionsInput{})
	if terr == nil || terr.Code != CodeUpstreamUnavailable {
		t.Errorf("err = %+v, want upstream_unavailable", terr)
	}
}

// --- set_subscription ---

func TestSetSubscription_CreatesFeedAndRule(t *testing.T) {
	feedPath := feedHashForTest(t, testFeedURL)
	client, captured := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules":   {body: "{}"},
		"/api/v2/rss/items":   {body: "{}"},
		"/api/v2/rss/addFeed": {body: ""},
		"/api/v2/rss/setRule": {body: ""},
	})
	out, terr := callSetSubscription(t, client, SetSubscriptionInput{
		Name:        "kura-show",
		FeedURL:     testFeedURL,
		Destination: "kura-inbox",
		Tags:        []string{"tvdb:12345"},
	})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if !out.Ok {
		t.Error("ok should be true")
	}
	if captured["/api/v2/rss/addFeed"].PostForm.Get("path") != feedPath {
		t.Errorf("addFeed path = %q, want %q", captured["/api/v2/rss/addFeed"].PostForm.Get("path"), feedPath)
	}
	if captured["/api/v2/rss/addFeed"].PostForm.Get("url") != testFeedURL {
		t.Errorf("addFeed url = %q", captured["/api/v2/rss/addFeed"].PostForm.Get("url"))
	}
	if captured["/api/v2/rss/setRule"].PostForm.Get("ruleName") != "kura-show" {
		t.Errorf("setRule ruleName = %q", captured["/api/v2/rss/setRule"].PostForm.Get("ruleName"))
	}
	ruleDef := captured["/api/v2/rss/setRule"].PostForm.Get("ruleDef")
	if !strings.Contains(ruleDef, `"save_path":"/mnt/kura"`) {
		t.Errorf("ruleDef should resolve destination to /mnt/kura; got %s", ruleDef)
	}
	if !strings.Contains(ruleDef, `"tags":["tvdb:12345"]`) {
		t.Errorf("ruleDef should embed tags; got %s", ruleDef)
	}
	if !strings.Contains(ruleDef, `"use_auto_tmm":false`) {
		t.Errorf("ruleDef should pin use_auto_tmm=false; got %s", ruleDef)
	}
}

func TestSetSubscription_SkipsAddFeedWhenFeedExists(t *testing.T) {
	feedPath := feedHashForTest(t, testFeedURL)
	client, captured := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules":   {body: "{}"},
		"/api/v2/rss/items":   {body: rssItemsBody(feedPath, testFeedURL, nil)},
		"/api/v2/rss/addFeed": {body: ""},
		"/api/v2/rss/setRule": {body: ""},
	})
	_, terr := callSetSubscription(t, client, SetSubscriptionInput{
		Name:    "other-show",
		FeedURL: testFeedURL,
		Tags:    []string{},
	})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if captured["/api/v2/rss/addFeed"].Method != "" {
		t.Error("addFeed should not be called when the feed already exists")
	}
}

func TestSetSubscription_RejectsFeedURLChangeOnExisting(t *testing.T) {
	priorFeedPath := feedHashForTest(t, "https://example.com/old.xml")
	rules := fmt.Sprintf(`{"kura-show":{"enabled":true,"affectedFeeds":[%q],"torrentParams":{"tags":[]}}}`, priorFeedPath)
	client, captured := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules":   {body: rules},
		"/api/v2/rss/items":   {body: "{}"},
		"/api/v2/rss/addFeed": {body: ""},
		"/api/v2/rss/setRule": {body: ""},
	})
	_, terr := callSetSubscription(t, client, SetSubscriptionInput{
		Name:    "kura-show",
		FeedURL: testFeedURL,
		Tags:    []string{},
	})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Fatalf("err = %+v, want invalid_argument", terr)
	}
	if captured["/api/v2/rss/setRule"].Method != "" {
		t.Error("setRule should not be called when feed_url change is rejected")
	}
	if captured["/api/v2/rss/addFeed"].Method != "" {
		t.Error("addFeed should not be called when feed_url change is rejected")
	}
}

func TestSetSubscription_TagsRequired(t *testing.T) {
	_, terr := callSetSubscription(t, nil, SetSubscriptionInput{
		Name:    "kura-show",
		FeedURL: testFeedURL,
		Tags:    nil,
	})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v, want invalid_argument", terr)
	}
}

func TestSetSubscription_TagWithCommaRejected(t *testing.T) {
	_, terr := callSetSubscription(t, nil, SetSubscriptionInput{
		Name:    "kura-show",
		FeedURL: testFeedURL,
		Tags:    []string{"bad,tag"},
	})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v, want invalid_argument", terr)
	}
}

func TestSetSubscription_EmptyNameRejected(t *testing.T) {
	_, terr := callSetSubscription(t, nil, SetSubscriptionInput{
		Name:    "",
		FeedURL: testFeedURL,
		Tags:    []string{},
	})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v, want invalid_argument", terr)
	}
}

func TestSetSubscription_UnknownDestinationRejected(t *testing.T) {
	feedPath := feedHashForTest(t, testFeedURL)
	client, captured := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules":   {body: "{}"},
		"/api/v2/rss/items":   {body: rssItemsBody(feedPath, testFeedURL, nil)},
		"/api/v2/rss/addFeed": {body: ""},
		"/api/v2/rss/setRule": {body: ""},
	})
	_, terr := callSetSubscription(t, client, SetSubscriptionInput{
		Name:        "kura-show",
		FeedURL:     testFeedURL,
		Destination: "bogus",
		Tags:        []string{},
	})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Fatalf("err = %+v, want invalid_argument", terr)
	}
	if captured["/api/v2/rss/setRule"].Method != "" {
		t.Error("setRule should not be called on unknown destination")
	}
}

// --- delete_subscription ---

func TestDeleteSubscription_RemovesRuleAndOrphanFeed(t *testing.T) {
	feedPath := feedHashForTest(t, testFeedURL)
	client, captured := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules":      {body: rssRulesBody("kura-show", feedPath, "", "", nil)},
		"/api/v2/rss/removeRule": {body: ""},
		"/api/v2/rss/removeItem": {body: ""},
	})
	out, terr := callDeleteSubscription(t, client, DeleteSubscriptionInput{Name: "kura-show"})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if !out.Ok {
		t.Error("ok should be true")
	}
	if captured["/api/v2/rss/removeRule"].PostForm.Get("ruleName") != "kura-show" {
		t.Errorf("removeRule ruleName = %q", captured["/api/v2/rss/removeRule"].PostForm.Get("ruleName"))
	}
	if captured["/api/v2/rss/removeItem"].PostForm.Get("path") != feedPath {
		t.Errorf("removeItem path = %q, want %q (orphan feed should be removed)",
			captured["/api/v2/rss/removeItem"].PostForm.Get("path"), feedPath)
	}
}

func TestDeleteSubscription_KeepsFeedWhenStillReferenced(t *testing.T) {
	feedPath := feedHashForTest(t, testFeedURL)
	// Two rules share the same feed path; deleting "kura-show" must
	// keep the feed since "other-show" still references it.
	rules := fmt.Sprintf(
		`{"kura-show":{"enabled":true,"affectedFeeds":[%q],"torrentParams":{"tags":[]}},"other-show":{"enabled":true,"affectedFeeds":[%q],"torrentParams":{"tags":[]}}}`,
		feedPath, feedPath,
	)
	client, captured := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules":      {body: rules},
		"/api/v2/rss/removeRule": {body: ""},
		"/api/v2/rss/removeItem": {body: ""},
	})
	_, terr := callDeleteSubscription(t, client, DeleteSubscriptionInput{Name: "kura-show"})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if captured["/api/v2/rss/removeItem"].Method != "" {
		t.Error("removeItem should not be called when another subscription still references the feed")
	}
}

func TestDeleteSubscription_UnknownNameReturnsNotFound(t *testing.T) {
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules": {body: "{}"},
	})
	_, terr := callDeleteSubscription(t, client, DeleteSubscriptionInput{Name: "missing"})
	if terr == nil || terr.Code != CodeUpstreamNotFound {
		t.Errorf("err = %+v, want upstream_not_found", terr)
	}
}

func TestDeleteSubscription_UnmanagedRuleReturnsNotFound(t *testing.T) {
	// Rule exists in qBittorrent but does not target our synthetic
	// feed-path prefix. Our surface treats it as not-found.
	rules := `{"manual":{"enabled":true,"affectedFeeds":["Anime\\Other"],"torrentParams":{"tags":[]}}}`
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/rss/rules": {body: rules},
	})
	_, terr := callDeleteSubscription(t, client, DeleteSubscriptionInput{Name: "manual"})
	if terr == nil || terr.Code != CodeUpstreamNotFound {
		t.Errorf("err = %+v, want upstream_not_found", terr)
	}
}

func TestDeleteSubscription_EmptyNameRejected(t *testing.T) {
	_, terr := callDeleteSubscription(t, nil, DeleteSubscriptionInput{Name: ""})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v, want invalid_argument", terr)
	}
}
