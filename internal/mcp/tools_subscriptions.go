package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	qbt "github.com/autobrr/go-qbittorrent"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wyvernzora/qbittorrent-mcp/internal/savepath"
)

// registerSubscriptions wires the 3 subscription tools onto s. A
// subscription bundles a qBittorrent RSS feed and the auto-download rule
// that filters its items into actual downloads — the two-layer model
// (feeds, rules) is fused so agents work with a single concept: "watch
// this URL, add matches to this destination with these tags".
//
// All handlers reach qBittorrent through the autobrr SDK
// (github.com/autobrr/go-qbittorrent v1.15.0), which covers the
// /api/v2/rss/* endpoints we need. No direct HTTP fallback is required.
func registerSubscriptions(s *mcpsdk.Server, client *qbt.Client, resolver *savepath.Resolver, logger *slog.Logger) {
	destHint := resolver.DescriptionHint()

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "list_subscriptions",
			Description: "List subscriptions. Each row carries the feed URL, the rule's filter fields (must_contain, episode_filter, ...), the destination alias the rule routes matched downloads to, the tags applied at creation, and a last_match_date summary. Set include_recent_items=true to also embed the most-recent feed items (capped by recent_items_limit, default 10, max 50).",
			Annotations: readOnlyAnnotations(),
		},
		wrap("list_subscriptions", logger, listSubscriptionsHandler(client, resolver)),
	)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "set_subscription",
			Description: "Upsert a subscription by name. Atomically creates (or replaces) the qBittorrent feed and the auto-download rule pointing at it. The feed_url is the only feed-side input; qbit-mcp derives a synthetic feed path 'qbit-mcp-<hash>' so duplicate feed_urls across subscriptions share storage transparently. Changing feed_url on an existing subscription is rejected — delete and re-create instead. tags is required on every call; passing a different tags array on replace re-tags FUTURE auto-added downloads only (existing matches keep their original tags — retroactive retag is out of scope). " + destHint,
			Annotations: mutatingAnnotations(false),
		},
		wrap("set_subscription", logger, setSubscriptionHandler(client, resolver, logger)),
	)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "delete_subscription",
			Description: "Delete a subscription by name. Removes the auto-download rule; the underlying feed is removed too unless another subscription still references the same feed_url.",
			Annotations: mutatingAnnotations(true),
		},
		wrap("delete_subscription", logger, deleteSubscriptionHandler(client, logger)),
	)
}

const (
	// feedPathPrefix is the qBittorrent RSS feed-name prefix for every
	// feed created by qbit-mcp. Flat (no folder) — keeps the synthetic
	// path single-token, so the RSS folder-separator question (qbit uses
	// backslash) never enters play.
	feedPathPrefix = "qbit-mcp-"

	defaultRecentItemsLimit = 10
	maxRecentItemsLimit     = 50
)

// feedPathForURL derives the synthetic qBittorrent RSS feed path for a
// given feed URL. Subscriptions that share a feed_url collide on this
// path, which is the dedupe mechanism — qBittorrent stores the feed once
// and multiple rules can reference it. The 16-hex-char prefix of the
// sha256 is enough collision resistance for the cardinalities a single
// qBittorrent instance handles in practice while staying short enough to
// browse comfortably in qbit's WebUI tree.
//
// URL is the literal dedupe key; no normalization is applied. Callers
// are responsible for using a consistent URL form.
func feedPathForURL(url string) string {
	sum := sha256.Sum256([]byte(url))
	return feedPathPrefix + hex.EncodeToString(sum[:])[:16]
}

// --- list_subscriptions ---

type ListSubscriptionsInput struct {
	IncludeRecentItems bool   `json:"include_recent_items,omitempty" jsonschema:"embed each subscription's most-recent feed items. default false because feeds can carry hundreds of entries."`
	RecentItemsLimit   int    `json:"recent_items_limit,omitempty" jsonschema:"max items per subscription when include_recent_items=true. default 10, max 50."`
	Since              string `json:"since,omitempty" jsonschema:"RFC3339 timestamp; with include_recent_items=true, only items with pub_date >= since are returned."`
}

type SubscriptionItem struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	PubDate string `json:"pub_date"`
}

type Subscription struct {
	Name           string             `json:"name"`
	FeedURL        string             `json:"feed_url"`
	Enabled        bool               `json:"enabled"`
	MustContain    string             `json:"must_contain,omitempty"`
	MustNotContain string             `json:"must_not_contain,omitempty"`
	UseRegex       bool               `json:"use_regex"`
	EpisodeFilter  string             `json:"episode_filter,omitempty"`
	SmartFilter    bool               `json:"smart_filter"`
	Destination    string             `json:"destination,omitempty"`
	SavePath       string             `json:"save_path,omitempty"`
	Tags           []string           `json:"tags"`
	IgnoreDays     int                `json:"ignore_days"`
	AddPaused      bool               `json:"add_paused"`
	FeedHasError   bool               `json:"feed_has_error"`
	LastMatchDate  string             `json:"last_match_date,omitempty"`
	RecentItems    []SubscriptionItem `json:"recent_items,omitempty"`
}

type ListSubscriptionsOutput struct {
	Subscriptions []Subscription `json:"subscriptions"`
}

func listSubscriptionsHandler(client *qbt.Client, resolver *savepath.Resolver) internalHandler[ListSubscriptionsInput, ListSubscriptionsOutput] {
	return func(ctx context.Context, in ListSubscriptionsInput) (ListSubscriptionsOutput, *ToolError) {
		empty := ListSubscriptionsOutput{Subscriptions: []Subscription{}}

		limit, terr := normalizeRecentItemsLimit(in.RecentItemsLimit)
		if terr != nil {
			return empty, terr
		}
		var sinceCutoff time.Time
		if in.Since != "" {
			t, err := time.Parse(time.RFC3339, in.Since)
			if err != nil {
				return empty, &ToolError{
					Code:      CodeInvalidArgument,
					Message:   "since must be RFC3339: " + err.Error(),
					Retriable: false,
				}
			}
			sinceCutoff = t
		}

		rules, err := client.GetRSSRulesCtx(ctx)
		if err != nil {
			return empty, errorFromSDK(err)
		}
		// withData=true returns inline article arrays, which we need
		// regardless of include_recent_items because last_match_date
		// already comes from rule.LastMatch and recent_items needs the
		// articles. The single fetch covers both.
		items, err := client.GetRSSItemsCtx(ctx, in.IncludeRecentItems)
		if err != nil {
			return empty, errorFromSDK(err)
		}

		out := ListSubscriptionsOutput{Subscriptions: []Subscription{}}
		names := make([]string, 0, len(rules))
		for name := range rules {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			rule := rules[name]
			if !ruleIsManaged(rule) {
				continue
			}
			feedPath := rule.AffectedFeeds[0]
			feed, _ := findFeedAtPath(items, feedPath)
			sub := projectSubscription(name, rule, feed, resolver)
			if in.IncludeRecentItems {
				sub.RecentItems = projectRecentItems(feed.Articles, limit, sinceCutoff)
			}
			out.Subscriptions = append(out.Subscriptions, sub)
		}
		return out, nil
	}
}

// ruleIsManaged tests whether a qBittorrent rule belongs to the
// subscription surface qbit-mcp exposes. We only surface rules with
// exactly one affected feed whose path is under the synthetic
// "qbit-mcp-" prefix; rules created via qBittorrent's WebUI directly,
// or rules targeting multiple feeds, are deliberately invisible.
func ruleIsManaged(rule qbt.RSSAutoDownloadRule) bool {
	if len(rule.AffectedFeeds) != 1 {
		return false
	}
	return strings.HasPrefix(rule.AffectedFeeds[0], feedPathPrefix)
}

// findFeedAtPath walks the hierarchical RSSItems map looking for a feed
// at the given slash-or-backslash-separated path. Returns the feed and
// ok=true when found. The path uses qBittorrent's native separator
// (backslash) but for our flat top-level entries either separator works
// because there is only one path component.
func findFeedAtPath(items qbt.RSSItems, path string) (qbt.RSSFeed, bool) {
	parts := splitFeedPath(path)
	cursor := items
	for i, part := range parts {
		raw, ok := cursor[part]
		if !ok {
			return qbt.RSSFeed{}, false
		}
		if i == len(parts)-1 {
			var feed qbt.RSSFeed
			if err := json.Unmarshal(raw, &feed); err == nil && feed.URL != "" {
				return feed, true
			}
			return qbt.RSSFeed{}, false
		}
		var nested qbt.RSSItems
		if err := json.Unmarshal(raw, &nested); err != nil {
			return qbt.RSSFeed{}, false
		}
		cursor = nested
	}
	return qbt.RSSFeed{}, false
}

// splitFeedPath handles both qBittorrent's native backslash separator
// and the forward-slash form some operator-side tools use. qbit-mcp's
// own feed paths are flat single-token strings so this is mostly a
// safety net.
func splitFeedPath(path string) []string {
	switch {
	case strings.Contains(path, `\`):
		return strings.Split(path, `\`)
	case strings.Contains(path, "/"):
		return strings.Split(path, "/")
	default:
		return []string{path}
	}
}

func projectSubscription(name string, rule qbt.RSSAutoDownloadRule, feed qbt.RSSFeed, resolver *savepath.Resolver) Subscription {
	savePath, tags, addPaused := extractRuleParams(rule)
	out := Subscription{
		Name:           name,
		FeedURL:        feed.URL,
		Enabled:        rule.Enabled,
		MustContain:    rule.MustContain,
		MustNotContain: rule.MustNotContain,
		UseRegex:       rule.UseRegex,
		EpisodeFilter:  rule.EpisodeFilter,
		SmartFilter:    rule.SmartFilter,
		SavePath:       savePath,
		Destination:    resolver.NameForPath(savePath),
		Tags:           tags,
		IgnoreDays:     rule.IgnoreDays,
		AddPaused:      addPaused,
		FeedHasError:   feed.HasError,
		LastMatchDate:  rule.LastMatch,
	}
	return out
}

// extractRuleParams pulls the save_path / tags / add_paused values out
// of the rule, preferring the modern TorrentParams shape and falling
// back to the legacy top-level fields qBittorrent still emits on older
// installs.
func extractRuleParams(rule qbt.RSSAutoDownloadRule) (savePath string, tags []string, addPaused bool) {
	tags = []string{}
	if rule.TorrentParams != nil {
		savePath = rule.TorrentParams.SavePath
		if rule.TorrentParams.Tags != nil {
			tags = rule.TorrentParams.Tags
		}
		if rule.TorrentParams.Stopped != nil {
			addPaused = *rule.TorrentParams.Stopped
		}
	}
	if savePath == "" {
		savePath = rule.SavePath
	}
	if !addPaused && rule.AddPaused != nil {
		addPaused = *rule.AddPaused
	}
	return savePath, tags, addPaused
}

func projectRecentItems(articles []qbt.RSSArticle, limit int, since time.Time) []SubscriptionItem {
	out := make([]SubscriptionItem, 0, limit)
	for _, a := range articles {
		if !since.IsZero() {
			t, err := parseArticleDate(a.Date)
			if err != nil || t.Before(since) {
				continue
			}
		}
		out = append(out, SubscriptionItem{
			Title:   a.Title,
			Link:    pickArticleLink(a),
			PubDate: a.Date,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// pickArticleLink prefers the torrent URL (magnet or .torrent) over the
// HTML link; the magnet form is what the rule will actually feed into
// qBittorrent on a match, so it is the more useful thing to surface.
func pickArticleLink(a qbt.RSSArticle) string {
	if a.TorrentURL != "" {
		return a.TorrentURL
	}
	return a.Link
}

// parseArticleDate accepts the date formats qBittorrent emits across
// versions. ISO 8601 with offset is the modern form; RFC1123Z appears on
// older builds. Failures fall through to the caller which treats them
// as "skip the since filter for this article".
func parseArticleDate(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", time.RFC1123Z, time.RFC1123} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date %q", s)
}

func normalizeRecentItemsLimit(in int) (int, *ToolError) {
	if in < 0 {
		return 0, &ToolError{Code: CodeInvalidArgument, Message: "recent_items_limit must be >= 0", Retriable: false}
	}
	if in == 0 {
		return defaultRecentItemsLimit, nil
	}
	if in > maxRecentItemsLimit {
		return maxRecentItemsLimit, nil
	}
	return in, nil
}

// --- set_subscription ---

type SetSubscriptionInput struct {
	Name           string   `json:"name" jsonschema:"unique subscription name; doubles as the underlying qBittorrent rule name."`
	FeedURL        string   `json:"feed_url" jsonschema:"RSS feed URL. Subscriptions sharing the same feed_url share storage transparently. Immutable for the lifetime of the subscription."`
	Enabled        *bool    `json:"enabled,omitempty" jsonschema:"enable or disable the rule; default true on create."`
	MustContain    *string  `json:"must_contain,omitempty" jsonschema:"item must contain this string or regex."`
	MustNotContain *string  `json:"must_not_contain,omitempty" jsonschema:"item must not contain this string or regex."`
	UseRegex       *bool    `json:"use_regex,omitempty" jsonschema:"treat must_contain / must_not_contain as regex."`
	EpisodeFilter  *string  `json:"episode_filter,omitempty" jsonschema:"qBittorrent episode-filter expression, e.g. '1x2;'."`
	SmartFilter    *bool    `json:"smart_filter,omitempty" jsonschema:"qBittorrent's deduplicating smart filter."`
	Destination    string   `json:"destination,omitempty" jsonschema:"save-destination alias name for matched downloads. Empty inherits qBittorrent's account default."`
	Tags           []string `json:"tags" jsonschema:"tags applied to every download the rule auto-adds. Required on every call. Editing on replace re-tags future matches only; existing matches keep their original tags."`
	IgnoreDays     *int     `json:"ignore_days,omitempty" jsonschema:"cool-down days between matches."`
	AddPaused      *bool    `json:"add_paused,omitempty" jsonschema:"add matched downloads in paused state."`
}

func setSubscriptionHandler(client *qbt.Client, resolver *savepath.Resolver, logger *slog.Logger) internalHandler[SetSubscriptionInput, OkOutput] {
	return func(ctx context.Context, in SetSubscriptionInput) (OkOutput, *ToolError) {
		if terr := validateSetSubscription(in); terr != nil {
			return OkOutput{}, terr
		}
		savePath, rerr := resolver.Resolve(in.Destination)
		if rerr != nil {
			return OkOutput{}, &ToolError{Code: CodeInvalidArgument, Message: rerr.Error(), Retriable: false}
		}

		feedPath := feedPathForURL(in.FeedURL)

		existingRules, err := client.GetRSSRulesCtx(ctx)
		if err != nil {
			return OkOutput{}, errorFromSDK(err)
		}
		if prior, ok := existingRules[in.Name]; ok && ruleIsManaged(prior) {
			if prior.AffectedFeeds[0] != feedPath {
				return OkOutput{}, &ToolError{
					Code:      CodeInvalidArgument,
					Message:   "feed_url is immutable on an existing subscription; delete and re-create to change it",
					Retriable: false,
				}
			}
		}

		feedExists, err := feedExistsAtPath(ctx, client, feedPath)
		if err != nil {
			return OkOutput{}, errorFromSDK(err)
		}

		auditMutation(ctx, logger, slog.LevelInfo, "subscription_set", []string{in.Name},
			slog.String("feed_url", in.FeedURL),
			slog.String("destination", in.Destination),
			slog.Any("tags", in.Tags),
		)

		if !feedExists {
			if err := client.AddRSSFeedCtx(ctx, in.FeedURL, feedPath); err != nil {
				return OkOutput{}, errorFromSDK(err)
			}
		}

		rule := buildRule(in, feedPath, savePath)
		if err := client.SetRSSRuleCtx(ctx, in.Name, rule); err != nil {
			return OkOutput{}, errorFromSDK(err)
		}
		return OkOutput{Ok: true}, nil
	}
}

func validateSetSubscription(in SetSubscriptionInput) *ToolError {
	if strings.TrimSpace(in.Name) == "" {
		return &ToolError{Code: CodeInvalidArgument, Message: "name is required", Retriable: false}
	}
	if strings.TrimSpace(in.FeedURL) == "" {
		return &ToolError{Code: CodeInvalidArgument, Message: "feed_url is required", Retriable: false}
	}
	if in.Tags == nil {
		return &ToolError{Code: CodeInvalidArgument, Message: "tags is required (pass [] for no tags)", Retriable: false}
	}
	for _, t := range in.Tags {
		if strings.Contains(t, ",") {
			return &ToolError{
				Code:      CodeInvalidArgument,
				Message:   fmt.Sprintf("tag %q contains a comma; qBittorrent stores tags CSV-encoded so commas inside a tag would corrupt the list", t),
				Retriable: false,
			}
		}
	}
	return nil
}

// feedExistsAtPath checks whether the feed at feedPath is already
// registered in qBittorrent. Used to decide whether set_subscription
// needs to call AddRSSFeed (first time) or skip it (sharing a feed with
// another subscription). withData=false is enough — we only need the
// path-to-URL mapping, not articles.
func feedExistsAtPath(ctx context.Context, client *qbt.Client, feedPath string) (bool, error) {
	items, err := client.GetRSSItemsCtx(ctx, false)
	if err != nil {
		return false, err
	}
	_, ok := findFeedAtPath(items, feedPath)
	return ok, nil
}

func buildRule(in SetSubscriptionInput, feedPath, savePath string) qbt.RSSAutoDownloadRule {
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	rule := qbt.RSSAutoDownloadRule{
		Enabled:       enabled,
		AffectedFeeds: []string{feedPath},
		TorrentParams: &qbt.RSSRuleTorrentParams{
			Tags:       in.Tags,
			SavePath:   savePath,
			UseAutoTMM: ptrBool(false),
		},
	}
	if in.MustContain != nil {
		rule.MustContain = *in.MustContain
	}
	if in.MustNotContain != nil {
		rule.MustNotContain = *in.MustNotContain
	}
	if in.UseRegex != nil {
		rule.UseRegex = *in.UseRegex
	}
	if in.EpisodeFilter != nil {
		rule.EpisodeFilter = *in.EpisodeFilter
	}
	if in.SmartFilter != nil {
		rule.SmartFilter = *in.SmartFilter
	}
	if in.IgnoreDays != nil {
		rule.IgnoreDays = *in.IgnoreDays
	}
	if in.AddPaused != nil {
		rule.TorrentParams.Stopped = ptrBool(*in.AddPaused)
	}
	return rule
}

func ptrBool(b bool) *bool { return &b }

// --- delete_subscription ---

type DeleteSubscriptionInput struct {
	Name string `json:"name" jsonschema:"name of the subscription to remove."`
}

func deleteSubscriptionHandler(client *qbt.Client, logger *slog.Logger) internalHandler[DeleteSubscriptionInput, OkOutput] {
	return func(ctx context.Context, in DeleteSubscriptionInput) (OkOutput, *ToolError) {
		if strings.TrimSpace(in.Name) == "" {
			return OkOutput{}, &ToolError{Code: CodeInvalidArgument, Message: "name is required", Retriable: false}
		}
		rules, err := client.GetRSSRulesCtx(ctx)
		if err != nil {
			return OkOutput{}, errorFromSDK(err)
		}
		rule, ok := rules[in.Name]
		if !ok || !ruleIsManaged(rule) {
			return OkOutput{}, &ToolError{
				Code:      CodeUpstreamNotFound,
				Message:   fmt.Sprintf("subscription %q not found", in.Name),
				Retriable: false,
			}
		}
		feedPath := rule.AffectedFeeds[0]

		auditMutation(ctx, logger, slog.LevelWarn, "subscription_delete", []string{in.Name},
			slog.String("feed_path", feedPath),
		)

		if err := client.RemoveRSSRuleCtx(ctx, in.Name); err != nil {
			return OkOutput{}, errorFromSDK(err)
		}
		if !feedStillReferenced(rules, in.Name, feedPath) {
			if err := client.RemoveRSSItemCtx(ctx, feedPath); err != nil {
				return OkOutput{}, errorFromSDK(err)
			}
		}
		return OkOutput{Ok: true}, nil
	}
}

// feedStillReferenced reports whether any managed rule other than the
// one we just removed (excludeName) still points at feedPath. Used by
// delete to decide whether to garbage-collect the synthetic feed.
func feedStillReferenced(rules qbt.RSSRules, excludeName, feedPath string) bool {
	for name, r := range rules {
		if name == excludeName {
			continue
		}
		if !ruleIsManaged(r) {
			continue
		}
		if r.AffectedFeeds[0] == feedPath {
			return true
		}
	}
	return false
}
