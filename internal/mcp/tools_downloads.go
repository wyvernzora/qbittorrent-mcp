package mcp

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"sort"
	"strings"

	qbt "github.com/autobrr/go-qbittorrent"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wyvernzora/qbittorrent-mcp/internal/savepath"
)

// registerDownloads wires the 3 download tools onto s:
// qbit_search_downloads, qbit_add_download, qbit_remove_downloads.
//
// No qbit_get_download — qbit_search_downloads with a single-hash
// query and the trackers/files include_fields keys covers the same
// projection depth.
// No pause/resume — operator concern, not an agent workflow
// (pause/resume fits cron/maintenance windows, not the
// add/observe/remove agent loop).
// No update — everything (destination, tags, name) is set at creation
// time via qbit_add_download; mid-life metadata churn is not a
// workflow.
func registerDownloads(s *mcpsdk.Server, client *qbt.Client, resolver *savepath.Resolver, logger *slog.Logger) {
	destHint := resolver.DescriptionHint()

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "qbit_search_downloads",
			Description: "Search downloads with filtering, sorting, and pagination. Default projection is lean (hash, name, state, progress, sizes, speeds, eta, ratio, tags, added_on). Use include_fields to opt into richer fields including save_path, magnet_uri, peer/seed counts, flags, ratio_limit, seeding_time. The special value include_fields=[\"all\"] enables every opt-in field except trackers/files. The trackers and files keys require single-hash selection (exactly one hash in hashes, no states/tags filter) to avoid N+1 fan-out. Default limit 50, max 200; paginate via offset.",
			Annotations: readOnlyAnnotations(),
		},
		wrap("qbit_search_downloads", logger, searchDownloadsHandler(client, resolver)),
	)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "qbit_add_download",
			Description: "Add a download by magnet URI (URLs and .torrent file uploads are not supported in v1). The hash is parsed from the magnet's xt=urn:btih: parameter and returned synchronously. Idempotent: re-adding a hash qBittorrent already knows about leaves the live download untouched and reports already_existed=true. The destination field selects a deploy-time-configured save destination by name; raw filesystem paths are not accepted. " + destHint,
			Annotations: mutatingAnnotations(false),
		},
		wrap("qbit_add_download", logger, addDownloadHandler(client, resolver, logger)),
	)
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "qbit_remove_downloads",
			Description: "Remove downloads from qBittorrent's tracking. Pass exactly one of hashes (explicit set) or filter (states/tags). Files on disk are not deleted — file lifecycle is handled by the operator (cron, kura's trash, manual rm). This tool only forgets the download from qbit's perspective.",
			Annotations: mutatingAnnotations(true),
		},
		wrap("qbit_remove_downloads", logger, removeDownloadsHandler(client, logger)),
	)
}

// --- qbit_search_downloads ---

type SearchDownloadsInput struct {
	States        []NormalizedState `json:"states,omitempty" jsonschema:"optional state filter; OR semantics across the array. one of downloading, seeding, paused, stalled, queued, checking, errored, unknown"`
	Tags          []string          `json:"tags,omitempty" jsonschema:"tag-pattern filter; OR semantics. each entry is a shell-style glob (path.Match: *, ?, [abc]); plain strings match exactly. example: ['tvdb:*'] finds every download tagged tvdb:NNNNN."`
	Hashes        []string          `json:"hashes,omitempty" jsonschema:"explicit set of hashes to return. when combined with states/tags, hashes are pre-filtered upstream and states/tags further restrict the result (AND semantics)."`
	Sort          string            `json:"sort,omitempty" jsonschema:"sort order; one of name_asc, name_desc, added_on_asc, added_on_desc (default), size_asc, size_desc, progress_asc, progress_desc, dlspeed_desc, eta_asc, ratio_desc"`
	Limit         int               `json:"limit,omitempty" jsonschema:"max downloads to return; default 50, max 200"`
	Offset        int               `json:"offset,omitempty" jsonschema:"page offset; default 0"`
	IncludeFields []string          `json:"include_fields,omitempty" jsonschema:"opt-in fields. lean defaults to none. valid: save_path, content_path, download_path, magnet_uri, completion_on, last_activity, total_uploaded, total_downloaded, total_size, seeds, seeds_incomplete, peers, tracker_count, auto_tmm, sequential, force_start, super_seeding, first_last_piece_prio, ratio_limit, seeding_time, seeding_time_limit, private, trackers, files. Special value 'all' expands to every field except trackers/files. trackers and files require single-hash selection."`
}

type SearchDownloadsOutput struct {
	Count     int        `json:"count"`
	HasMore   bool       `json:"has_more"`
	Downloads []Download `json:"downloads"`
}

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 200
	qbtETAUnknown      = 8640000 // qBittorrent's "100 days" sentinel for unknown ETA
)

// sortSpec maps a public sort enum value to qBittorrent's native sort field
// plus the Reverse flag the SDK passes upstream.
type sortSpec struct {
	field   string
	reverse bool
}

var searchDownloadsSortMap = map[string]sortSpec{
	"name_asc":      {"name", false},
	"name_desc":     {"name", true},
	"added_on_asc":  {"added_on", false},
	"added_on_desc": {"added_on", true},
	"size_asc":      {"size", false},
	"size_desc":     {"size", true},
	"progress_asc":  {"progress", false},
	"progress_desc": {"progress", true},
	"dlspeed_desc":  {"dlspeed", true},
	"eta_asc":       {"eta", false},
	"ratio_desc":    {"ratio", true},
}

// downloadFieldSetter writes one opt-in field on the wire Download from the
// corresponding upstream field. Each include_fields value maps to exactly
// one setter; validation rejects unknown keys before this map is consulted.
type downloadFieldSetter func(out *Download, t qbt.Torrent)

// downloadFieldSetters is the single source of truth for opt-in field
// projection. trackers/files have no setter — they are populated by
// per-hash upstream calls in the handler itself, gated on single-hash
// selection.
var downloadFieldSetters = map[string]downloadFieldSetter{
	"save_path":             func(out *Download, t qbt.Torrent) { out.SavePath = t.SavePath },
	"content_path":          func(out *Download, t qbt.Torrent) { out.ContentPath = t.ContentPath },
	"download_path":         func(out *Download, t qbt.Torrent) { out.DownloadPath = t.DownloadPath },
	"magnet_uri":            func(out *Download, t qbt.Torrent) { out.MagnetURI = t.MagnetURI },
	"completion_on":         func(out *Download, t qbt.Torrent) { out.CompletionOn = t.CompletionOn },
	"last_activity":         func(out *Download, t qbt.Torrent) { out.LastActivity = t.LastActivity },
	"total_uploaded":        func(out *Download, t qbt.Torrent) { out.TotalUploaded = t.Uploaded },
	"total_downloaded":      func(out *Download, t qbt.Torrent) { out.TotalDownloaded = t.Downloaded },
	"total_size":            func(out *Download, t qbt.Torrent) { out.TotalSize = t.TotalSize },
	"seeds":                 func(out *Download, t qbt.Torrent) { out.SeedsComplete = t.NumComplete },
	"seeds_incomplete":      func(out *Download, t qbt.Torrent) { out.SeedsIncomplete = t.NumIncomplete },
	"peers":                 func(out *Download, t qbt.Torrent) { out.PeersConnected = t.NumSeeds + t.NumLeechs },
	"tracker_count":         func(out *Download, t qbt.Torrent) { out.TrackerCount = t.TrackersCount },
	"auto_tmm":              func(out *Download, t qbt.Torrent) { v := t.AutoManaged; out.AutoTMM = &v },
	"sequential":            func(out *Download, t qbt.Torrent) { v := t.SequentialDownload; out.Sequential = &v },
	"force_start":           func(out *Download, t qbt.Torrent) { v := t.ForceStart; out.ForceStart = &v },
	"super_seeding":         func(out *Download, t qbt.Torrent) { v := t.SuperSeeding; out.SuperSeeding = &v },
	"first_last_piece_prio": func(out *Download, t qbt.Torrent) { v := t.FirstLastPiecePrio; out.FirstLastPiecePrio = &v },
	"ratio_limit":           func(out *Download, t qbt.Torrent) { out.RatioLimit = t.RatioLimit },
	"seeding_time":          func(out *Download, t qbt.Torrent) { out.SeedingTime = t.SeedingTime },
	"seeding_time_limit":    func(out *Download, t qbt.Torrent) { out.SeedingTimeLimit = t.SeedingTimeLimit },
	"private":               func(out *Download, t qbt.Torrent) { v := t.Private; out.Private = &v },
}

// validIncludeFields holds every accepted include_fields value, including
// the per-hash enrichments (trackers, files) that have no field-setter.
// resolveIncludeFields consults this rather than downloadFieldSetters so
// trackers/files validate as known.
var validIncludeFields = func() map[string]bool {
	out := map[string]bool{"trackers": true, "files": true}
	for k := range downloadFieldSetters {
		out[k] = true
	}
	return out
}()

// searchDownloadsRequest is the validated, ready-to-execute form of a
// SearchDownloadsInput. prepareSearchDownloads produces it after every
// validation rule, keeping the handler body thin.
type searchDownloadsRequest struct {
	opts       qbt.TorrentFilterOptions
	includeSet map[string]bool
	limit      int
	offset     int
}

func searchDownloadsHandler(client *qbt.Client, resolver *savepath.Resolver) internalHandler[SearchDownloadsInput, SearchDownloadsOutput] {
	return func(ctx context.Context, in SearchDownloadsInput) (SearchDownloadsOutput, *ToolError) {
		empty := SearchDownloadsOutput{Downloads: []Download{}}

		req, terr := prepareSearchDownloads(in)
		if terr != nil {
			return empty, terr
		}

		downloads, err := client.GetTorrentsCtx(ctx, req.opts)
		if err != nil {
			return empty, errorFromSDK(err)
		}

		filtered, terr := filterDownloads(downloads, in.States, in.Tags)
		if terr != nil {
			return empty, terr
		}

		page, hasMore := paginateDownloads(filtered, req.offset, req.limit)
		out := SearchDownloadsOutput{
			Count:     len(page),
			HasMore:   hasMore,
			Downloads: make([]Download, 0, len(page)),
		}
		for _, t := range page {
			d := projectDownload(t, req.includeSet, resolver)
			if req.includeSet["trackers"] {
				if terr := fetchTrackers(ctx, client, t.Hash, &d); terr != nil {
					return empty, terr
				}
			}
			if req.includeSet["files"] {
				if terr := fetchFiles(ctx, client, t.Hash, &d); terr != nil {
					return empty, terr
				}
			}
			out.Downloads = append(out.Downloads, d)
		}
		return out, nil
	}
}

// prepareSearchDownloads validates input and assembles the upstream filter
// options plus the resolved include-fields set and clamped limit/offset.
// Enforces the single-hash rule on trackers/files include_fields.
func prepareSearchDownloads(in SearchDownloadsInput) (searchDownloadsRequest, *ToolError) {
	limit, terr := normalizeSearchLimit(in.Limit)
	if terr != nil {
		return searchDownloadsRequest{}, terr
	}
	offset, terr := normalizeSearchOffset(in.Offset)
	if terr != nil {
		return searchDownloadsRequest{}, terr
	}
	sortField, reverse, terr := resolveSort(in.Sort)
	if terr != nil {
		return searchDownloadsRequest{}, terr
	}
	if terr := validateStates(in.States); terr != nil {
		return searchDownloadsRequest{}, terr
	}
	if terr := validateTagPatterns(in.Tags); terr != nil {
		return searchDownloadsRequest{}, terr
	}
	includeSet, terr := resolveIncludeFields(in.IncludeFields)
	if terr != nil {
		return searchDownloadsRequest{}, terr
	}
	if (includeSet["trackers"] || includeSet["files"]) && !isSingleHashSelection(in) {
		return searchDownloadsRequest{}, &ToolError{
			Code:      CodeInvalidArgument,
			Message:   "trackers and files require single-hash selection: exactly one entry in hashes, no states or tags filter; otherwise per-hash fetches would fan out N+1 across the result set",
			Retriable: false,
		}
	}
	opts := qbt.TorrentFilterOptions{Sort: sortField, Reverse: reverse}
	if len(in.Hashes) > 0 {
		opts.Hashes = in.Hashes
	}
	return searchDownloadsRequest{opts: opts, includeSet: includeSet, limit: limit, offset: offset}, nil
}

func isSingleHashSelection(in SearchDownloadsInput) bool {
	return len(in.Hashes) == 1 && len(in.States) == 0 && len(in.Tags) == 0
}

// filterDownloads applies the state-set and tag-glob filters that the qbit
// API cannot express natively. Tag patterns are assumed pre-validated by
// validateTagPatterns; a re-failure here is treated as an internal error.
func filterDownloads(downloads []qbt.Torrent, states []NormalizedState, tagPatterns []string) ([]qbt.Torrent, *ToolError) {
	stateSet := make(map[NormalizedState]bool, len(states))
	for _, s := range states {
		stateSet[s] = true
	}
	out := make([]qbt.Torrent, 0, len(downloads))
	for _, t := range downloads {
		if len(stateSet) > 0 && !stateSet[normalizeState(t.State)] {
			continue
		}
		if len(tagPatterns) > 0 {
			ok, err := matchAnyTag(tagPatterns, splitTags(t.Tags))
			if err != nil {
				return nil, &ToolError{Code: CodeInternal, Message: err.Error(), Retriable: false}
			}
			if !ok {
				continue
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// paginateDownloads slices the filtered set per offset+limit and reports
// whether more downloads exist past the returned page. Out-of-range offsets
// yield an empty page (not an error).
func paginateDownloads(filtered []qbt.Torrent, offset, limit int) ([]qbt.Torrent, bool) {
	total := len(filtered)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return filtered[start:end], end < total
}

func normalizeSearchLimit(in int) (int, *ToolError) {
	if in < 0 {
		return 0, &ToolError{Code: CodeInvalidArgument, Message: "limit must be >= 0", Retriable: false}
	}
	if in == 0 {
		return defaultSearchLimit, nil
	}
	if in > maxSearchLimit {
		return maxSearchLimit, nil
	}
	return in, nil
}

func normalizeSearchOffset(in int) (int, *ToolError) {
	if in < 0 {
		return 0, &ToolError{Code: CodeInvalidArgument, Message: "offset must be >= 0", Retriable: false}
	}
	return in, nil
}

func resolveSort(s string) (field string, reverse bool, terr *ToolError) {
	if s == "" {
		s = "added_on_desc"
	}
	spec, ok := searchDownloadsSortMap[s]
	if !ok {
		return "", false, &ToolError{
			Code:      CodeInvalidArgument,
			Message:   fmt.Sprintf("unknown sort %q; valid: %s", s, validSortNames()),
			Retriable: false,
		}
	}
	return spec.field, spec.reverse, nil
}

func validSortNames() string {
	out := make([]string, 0, len(searchDownloadsSortMap))
	for k := range searchDownloadsSortMap {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// validateStates normalizes each entry to lowercase before checking the
// enum, then writes the lowercased value back so downstream filtering
// matches the canonical form. The model commonly emits "Downloading" /
// "DOWNLOADING" / "downloading" interchangeably depending on prior
// context; rejecting non-lowercase variants would be a footgun for no
// reason — the underlying state strings are case-insensitive on the
// wire.
func validateStates(states []NormalizedState) *ToolError {
	for i, s := range states {
		lower := NormalizedState(strings.ToLower(string(s)))
		if !isValidNormalizedState(lower) {
			return &ToolError{
				Code:      CodeInvalidArgument,
				Message:   fmt.Sprintf("unknown state %q; valid: downloading, seeding, paused, stalled, queued, checking, errored, unknown", s),
				Retriable: false,
			}
		}
		states[i] = lower
	}
	return nil
}

func isValidNormalizedState(s NormalizedState) bool {
	switch s {
	case StateDownloading, StateSeeding, StatePaused, StateStalled,
		StateQueued, StateChecking, StateErrored, StateUnknown:
		return true
	}
	return false
}

func validateTagPatterns(patterns []string) *ToolError {
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			return &ToolError{
				Code:      CodeInvalidArgument,
				Message:   fmt.Sprintf("invalid tag pattern %q: %v", p, err),
				Retriable: false,
			}
		}
	}
	return nil
}

// resolveIncludeFields validates and expands the include_fields list. The
// special value "all" expands to every field-level key (every
// downloadFieldSetters entry) but NOT trackers/files, which always require
// explicit opt-in and trigger the single-hash constraint.
func resolveIncludeFields(in []string) (map[string]bool, *ToolError) {
	out := make(map[string]bool, len(in))
	for _, f := range in {
		if f == "all" {
			for k := range downloadFieldSetters {
				out[k] = true
			}
			continue
		}
		if !validIncludeFields[f] {
			return nil, &ToolError{
				Code:      CodeInvalidArgument,
				Message:   fmt.Sprintf("unknown include_fields value %q; valid: %s, all", f, validIncludeFieldNames()),
				Retriable: false,
			}
		}
		out[f] = true
	}
	return out, nil
}

func validIncludeFieldNames() string {
	out := make([]string, 0, len(validIncludeFields))
	for k := range validIncludeFields {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// splitTags parses qBittorrent's comma-separated Tags string into a slice.
// Whitespace around each tag is trimmed; empty entries are dropped. An
// empty input yields a non-nil empty slice so JSON output is `[]` rather
// than `null`.
func splitTags(csv string) []string {
	out := []string{}
	if csv == "" {
		return out
	}
	for _, p := range strings.Split(csv, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// normalizeETA collapses qBittorrent's 8640000-second "unknown" sentinel
// to -1 so agents have a single value to branch on.
func normalizeETA(eta int64) int64 {
	if eta == qbtETAUnknown {
		return -1
	}
	return eta
}

// projectDownload maps an autobrr qbt.Torrent into the lean MCP wire shape,
// filling opt-in fields per the include set. Trackers and Files are NOT
// populated here — the handler fetches them per-hash when requested.
//
// Path-shaped fields (save_path, content_path, download_path) are
// post-processed through resolver.NameForPathPrefixed when they're
// part of the include set: an absolute path that lives under a
// configured alias root is rewritten to the "<alias>:<relpath>" form
// (or just "<alias>" when the path equals the root exactly). Paths
// outside every configured alias are echoed raw.
func projectDownload(t qbt.Torrent, include map[string]bool, resolver *savepath.Resolver) Download {
	out := Download{
		Hash:               t.Hash,
		Name:               t.Name,
		State:              normalizeState(t.State),
		Progress:           t.Progress,
		SizeBytes:          t.Size,
		DlspeedBytesPerSec: t.DlSpeed,
		UpspeedBytesPerSec: t.UpSpeed,
		EtaSeconds:         normalizeETA(t.ETA),
		Ratio:              t.Ratio,
		Tags:               splitTags(t.Tags),
		AddedOn:            t.AddedOn,
	}
	for key := range include {
		if setter, ok := downloadFieldSetters[key]; ok {
			setter(&out, t)
		}
	}
	if include["save_path"] {
		out.SavePath = prefixed(resolver, out.SavePath)
	}
	if include["content_path"] {
		out.ContentPath = prefixed(resolver, out.ContentPath)
	}
	if include["download_path"] {
		out.DownloadPath = prefixed(resolver, out.DownloadPath)
	}
	return out
}

// prefixed rewrites a qBittorrent absolute path into the wire form
// used by every tool output projection (see
// savepath.Resolver.NameForPathPrefixed). Empty input stays empty;
// every non-empty input becomes either "<alias>", "<alias>:<relpath>",
// or "unspecified:<rest>".
func prefixed(resolver *savepath.Resolver, path string) string {
	if path == "" {
		return ""
	}
	return resolver.NameForPathPrefixed(path)
}

// trackerStatusString converts autobrr's integer TrackerStatus enum into a
// readable wire value. Maps to qBittorrent's documented categories;
// unknown codes pass through as "unknown".
func trackerStatusString(s qbt.TrackerStatus) string {
	switch s {
	case qbt.TrackerStatusDisabled:
		return "disabled"
	case qbt.TrackerStatusNotContacted:
		return "not_contacted"
	case qbt.TrackerStatusOK:
		return "working"
	case qbt.TrackerStatusUpdating:
		return "updating"
	case qbt.TrackerStatusNotWorking:
		return "not_working"
	default:
		return "unknown"
	}
}

func projectTracker(t qbt.TorrentTracker) DownloadTracker {
	return DownloadTracker{
		URL:         t.Url,
		Status:      trackerStatusString(t.Status),
		NumPeers:    t.NumPeers,
		NumSeeds:    t.NumSeeds,
		NumLeechers: t.NumLeechers,
		Message:     t.Message,
	}
}

func projectFile(f qbt.TorrentFile) DownloadFile {
	return DownloadFile{
		Name:     f.Name,
		Size:     f.Size,
		Progress: float64(f.Progress),
		Priority: f.Priority,
	}
}

func fetchTrackers(ctx context.Context, client *qbt.Client, hash string, d *Download) *ToolError {
	trackers, err := client.GetTorrentTrackersCtx(ctx, hash)
	if err != nil {
		return errorFromSDK(err)
	}
	d.Trackers = make([]DownloadTracker, 0, len(trackers))
	for _, tr := range trackers {
		d.Trackers = append(d.Trackers, projectTracker(tr))
	}
	return nil
}

func fetchFiles(ctx context.Context, client *qbt.Client, hash string, d *Download) *ToolError {
	files, err := client.GetFilesInformationCtx(ctx, hash)
	if err != nil {
		return errorFromSDK(err)
	}
	if files == nil {
		d.Files = []DownloadFile{}
		return nil
	}
	d.Files = make([]DownloadFile, 0, len(*files))
	for _, f := range *files {
		d.Files = append(d.Files, projectFile(f))
	}
	return nil
}

// --- qbit_add_download ---

type AddDownloadInput struct {
	Magnet      string   `json:"magnet" jsonschema:"magnet URI with xt=urn:btih:<hash> parameter. URLs and torrent-file uploads are not supported in v1."`
	Tags        []string `json:"tags,omitempty" jsonschema:"tags to apply on add; unknown tags are auto-created. tag names must not contain commas (qBittorrent stores tag lists CSV-encoded)."`
	Destination string   `json:"destination,omitempty" jsonschema:"save-destination alias. Accepts '<alias>' for the alias root or '<alias>:<relpath>' to target a subdirectory under the root (relpath must not start with '/' or contain '..'). Reserved name 'unspecified' rejected — output-only sentinel. Empty inherits qBittorrent's account default."`
	Rename      string   `json:"rename,omitempty" jsonschema:"rename download inside qBittorrent on add (display name override)"`
}

type AddDownloadOutput struct {
	Hash           string `json:"hash"`
	Accepted       bool   `json:"accepted"`
	AlreadyExisted bool   `json:"already_existed"`
}

func addDownloadHandler(client *qbt.Client, resolver *savepath.Resolver, logger *slog.Logger) internalHandler[AddDownloadInput, AddDownloadOutput] {
	return func(ctx context.Context, in AddDownloadInput) (AddDownloadOutput, *ToolError) {
		empty := AddDownloadOutput{}

		hash, terr := parseMagnetHash(in.Magnet)
		if terr != nil {
			return empty, terr
		}
		if terr := validateAddDownloadTags(in.Tags); terr != nil {
			return empty, terr
		}
		savePath, rerr := resolver.Resolve(in.Destination)
		if rerr != nil {
			return empty, &ToolError{Code: CodeInvalidArgument, Message: rerr.Error(), Retriable: false}
		}

		// Idempotent pre-check: qBittorrent's POST /torrents/add returns
		// "Ok." even when the hash is already present, so the response
		// alone can't distinguish "new add" from "re-add of existing
		// download". We probe via /torrents/info?hashes=<h> before the
		// add — if the hash is already known, we skip the upstream call
		// entirely, leaving the live download (tags, destination,
		// progress) untouched. The audit log carries already_existed so
		// agents and operators can tell the noop case apart.
		existing, err := client.GetTorrentsCtx(ctx, qbt.TorrentFilterOptions{Hashes: []string{hash}})
		if err != nil {
			return empty, errorFromSDK(err)
		}
		alreadyExisted := len(existing) > 0

		auditMutation(ctx, logger, slog.LevelInfo, "add", []string{hash},
			slog.String("destination", in.Destination),
			slog.Any("tags", in.Tags),
			slog.Bool("already_existed", alreadyExisted),
		)
		if alreadyExisted {
			return AddDownloadOutput{Hash: hash, Accepted: true, AlreadyExisted: true}, nil
		}

		// Force autoTMM=false on every add. qBittorrent's Automatic Torrent
		// Management auto-routes save_path based on category — if it were
		// left on, the destination alias we just resolved would be silently
		// overridden by the operator's category routing. Pinning false
		// preserves the security boundary the destination alias system
		// exists to enforce.
		opts := map[string]string{"autoTMM": "false"}
		if savePath != "" {
			opts["savepath"] = savePath
		}
		if len(in.Tags) > 0 {
			opts["tags"] = strings.Join(in.Tags, ",")
		}
		if in.Rename != "" {
			opts["rename"] = in.Rename
		}

		if err := client.AddTorrentFromUrlCtx(ctx, in.Magnet, opts); err != nil {
			return empty, errorFromSDK(err)
		}
		return AddDownloadOutput{Hash: hash, Accepted: true, AlreadyExisted: false}, nil
	}
}

// parseMagnetHash extracts and normalizes the btih info-hash from a magnet
// URI. Accepts 40-char hex (returned lowercased) and 32-char base32
// (decoded to 20 bytes and re-encoded as hex). Returns invalid_argument
// for any other shape — missing scheme, no xt=urn:btih:, malformed hash.
func parseMagnetHash(magnet string) (string, *ToolError) {
	if magnet == "" {
		return "", &ToolError{Code: CodeInvalidArgument, Message: "magnet is required", Retriable: false}
	}
	if !strings.HasPrefix(magnet, "magnet:") {
		return "", &ToolError{Code: CodeInvalidArgument, Message: "magnet must start with 'magnet:'", Retriable: false}
	}
	q := strings.TrimPrefix(magnet, "magnet:")
	q = strings.TrimPrefix(q, "?")
	values, err := url.ParseQuery(q)
	if err != nil {
		return "", &ToolError{Code: CodeInvalidArgument, Message: "magnet query string invalid: " + err.Error(), Retriable: false}
	}
	for _, xt := range values["xt"] {
		const prefix = "urn:btih:"
		if !strings.HasPrefix(xt, prefix) {
			continue
		}
		raw := strings.TrimPrefix(xt, prefix)
		if normalized, ok := normalizeBtihHash(raw); ok {
			return normalized, nil
		}
	}
	return "", &ToolError{Code: CodeInvalidArgument, Message: "magnet missing xt=urn:btih:<hash> with a valid 40-hex or 32-base32 hash", Retriable: false}
}

// normalizeBtihHash returns the 40-char lowercase hex form of a btih hash,
// converting from 32-char base32 when needed. Returns ok=false for any
// other length or invalid encoding.
func normalizeBtihHash(h string) (string, bool) {
	switch len(h) {
	case 40:
		lower := strings.ToLower(h)
		for i := 0; i < len(lower); i++ {
			c := lower[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return "", false
			}
		}
		return lower, true
	case 32:
		raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(h))
		if err != nil || len(raw) != 20 {
			return "", false
		}
		return hex.EncodeToString(raw), true
	default:
		return "", false
	}
}

func validateAddDownloadTags(tags []string) *ToolError {
	for _, t := range tags {
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

// --- qbit_remove_downloads ---

// HashesOrFilter is the bulk-op selector: pass either Hashes or Filter,
// never both, never neither. resolveTargets returns invalid_argument when
// the rule is violated.
type HashesOrFilter struct {
	Hashes []string        `json:"hashes,omitempty" jsonschema:"explicit set of download hashes to operate on"`
	Filter *FilterCriteria `json:"filter,omitempty" jsonschema:"select targets by state/tags instead of explicit hashes. mutually exclusive with hashes."`
}

// resolveTargets validates a HashesOrFilter selector and resolves it to a
// concrete hash list. For the filter path it pre-fetches every download
// and applies the same client-side filter logic list_downloads uses.
// Returns an empty slice (not an error) when the filter resolves to zero
// matches — caller should treat that as "no work to do" and skip the
// upstream mutation call.
func resolveTargets(ctx context.Context, client *qbt.Client, sel HashesOrFilter) ([]string, *ToolError) {
	hasHashes := len(sel.Hashes) > 0
	hasFilter := sel.Filter != nil
	switch {
	case hasHashes && hasFilter:
		return nil, &ToolError{
			Code:      CodeInvalidArgument,
			Message:   "pass exactly one of hashes or filter, not both",
			Retriable: false,
		}
	case !hasHashes && !hasFilter:
		return nil, &ToolError{
			Code:      CodeInvalidArgument,
			Message:   "pass either hashes (explicit set) or filter (states/tags); refusing to operate on every download",
			Retriable: false,
		}
	case sel.Hashes != nil && len(sel.Hashes) == 0:
		return nil, &ToolError{
			Code:      CodeInvalidArgument,
			Message:   "hashes is empty; refusing to operate on every download",
			Retriable: false,
		}
	}

	if hasHashes {
		return sel.Hashes, nil
	}

	if terr := validateStates(sel.Filter.States); terr != nil {
		return nil, terr
	}
	if terr := validateTagPatterns(sel.Filter.Tags); terr != nil {
		return nil, terr
	}
	downloads, err := client.GetTorrentsCtx(ctx, qbt.TorrentFilterOptions{})
	if err != nil {
		return nil, errorFromSDK(err)
	}
	filtered, terr := filterDownloads(downloads, sel.Filter.States, sel.Filter.Tags)
	if terr != nil {
		return nil, terr
	}
	out := make([]string, 0, len(filtered))
	for _, t := range filtered {
		out = append(out, t.Hash)
	}
	return out, nil
}

type RemoveDownloadsInput struct {
	HashesOrFilter
}

// removeDownloadsHandler removes downloads from qBittorrent's tracking.
// Files on disk are intentionally left intact — deleteFiles is hardcoded
// false so agents cannot reach through and delete arbitrary paths under
// destination aliases. File lifecycle is an operator concern (cron, kura
// trash, manual rm). Audit-logged at WARN since "qbit forgot this
// download" is the kind of event operators investigate when something
// downstream breaks.
func removeDownloadsHandler(client *qbt.Client, logger *slog.Logger) internalHandler[RemoveDownloadsInput, AffectedOutput] {
	return func(ctx context.Context, in RemoveDownloadsInput) (AffectedOutput, *ToolError) {
		hashes, terr := resolveTargets(ctx, client, in.HashesOrFilter)
		if terr != nil {
			return AffectedOutput{AffectedHashes: []string{}}, terr
		}
		if len(hashes) == 0 {
			return AffectedOutput{AffectedHashes: []string{}}, nil
		}
		auditMutation(ctx, logger, slog.LevelWarn, "remove", hashes)
		if err := client.DeleteTorrentsCtx(ctx, hashes, false); err != nil {
			return AffectedOutput{AffectedHashes: []string{}}, errorFromSDK(err)
		}
		return AffectedOutput{AffectedCount: len(hashes), AffectedHashes: hashes}, nil
	}
}
