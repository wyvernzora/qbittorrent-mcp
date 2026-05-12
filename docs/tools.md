# Tool surface

Design spec for the MCP tools `qbit-mcp` exposes. Ten tools across three groups: downloads (3), tags (1), RSS (6).

Design priorities:

- **Caller context budget is finite.** Tool descriptions stay short; outputs use lean default projections with opt-in expansion; lists default to 50 results, max 200.
- **Discrete narrow tools, not fat polymorphic ones.** Agents pick narrow tools more reliably than discriminated-union schemas.
- **Agent intent over qBittorrent mechanism.** The surface reflects the agent's mental model of "manage downloads" — observe, add, remove — not qbit's torrent/feed/rule abstraction. Pause/resume, mid-life updates, and a separate single-hash get all fold away because they don't match any real agent workflow.
- **Magnet-only `add_download`.** No URL or .torrent file uploads in v1 — keeps the input shape small and the handler synchronous (hash is known before the qbit call).
- **Filter-vs-hashes mutual exclusion on bulk ops.** Caller passes either explicit `hashes[]` or a `filter` object, never both. Forgetting `hashes` cannot accidentally remove every download.
- **Normalized state enum.** qBittorrent's 21 raw states collapse to 8 logical buckets the agent cares about.

## Conventions

### Destinations (save-path aliases)

Tools that direct download storage (`add_download`, `set_rss_rule`) **do not accept arbitrary filesystem paths**. Callers pass the *name* of a deploy-time-configured alias via a `destination` (or `set_destination`) field; the server resolves the name to a path before calling qBittorrent. Untrusted agents cannot redirect download storage outside the configured set.

The operator declares aliases at boot time via `--save-paths` / `QBITTORRENT_SAVE_PATHS`:

```
--save-paths='kura-inbox=/mnt/kura,downloads=/mnt/downloads'
```

Format: `name=path,name=path`. Names match `[a-z0-9][a-z0-9-]*`. Empty input is allowed (no aliases configured); in that case, tools that accept a `destination` only accept the empty string, which means "leave save_path unset, qBittorrent uses its account default".

Tool descriptions include the current alias list at session start, so agents see exactly which names are valid:

```
Valid destinations: downloads, kura-inbox. Leave empty to use qBittorrent's account default.
```

Output projections still report qBittorrent's raw `save_path` — that is what qbit actually stores. Agents read truth on the way out, pick aliases on the way in.

Unknown destination names return `invalid_argument`. Reverse-lookup (raw `save_path` → alias name) is not exposed in v1.

### Audit logging

Every mutation tool (`add_download`, `remove_downloads`) emits a structured slog record **before** the upstream call so the action is visible even when upstream rejects the request. The records share a fixed shape:

```
msg=tool audit
audit=true
action=<verb>        ← add | remove
hashes=[h1 h2 ...]
count=N
[tool-specific extras]
```

Severity differentiates ops so log aggregators filtering on level can surface the destructive ones:

| Action | Level | Why |
|---|---|---|
| `add` | `INFO` | Reversible by `remove_downloads`. |
| `remove` | `WARN` | Even without on-disk deletion, "qbit forgot this download" is the kind of event operators investigate when something downstream breaks. |

Operators investigating "what did the agent do" can `grep audit=true` on the structured JSON stderr stream. The `hashes` field carries the full hash list so per-hash forensics work too.

`wrap`'s per-call timing log (logged at INFO with `tool=<name>` and `duration_ms`) continues to capture every tool call including reads. The audit layer is additive — finer-grained, mutation-only.

### Tag-pattern matching

`tags` filter fields on `list_downloads` and `remove_downloads.filter` accept shell-style globs using Go's `path.Match` syntax:

| Pattern token | Matches |
|---|---|
| `*` | any run of characters |
| `?` | exactly one character |
| `[abc]` | any one of `a`, `b`, `c` |
| `[a-z]` | any character in the range |
| plain string | exact tag name |

OR semantics across the patterns list — a download is included if any pattern matches any of its tags.

Use case: dmhy-mcp tags downloads it adds with `tvdb:<series-id>`. `list_downloads` with `tags: ["tvdb:*"]` returns every kura-related job. `tags: ["tvdb:12345", "tvdb:67890"]` returns just those two series (literal match).

Mutation fields (`tags` on `add_download`) are **literal tag names**, not patterns — qBittorrent's API requires exact strings.

Malformed patterns return `invalid_argument` naming the offending entry.

### Hash format

Full 40-char SHA-1 lowercase hex (qBittorrent's `infohash_v1` form). Echoed unchanged from upstream — no truncation, no normalization beyond what qbit returns. Agents need exact match for follow-up calls.

### Numeric units

Sizes in bytes, speeds in bytes-per-second, durations in seconds, timestamps as epoch seconds. No humanized strings. Agents do their own formatting; locale-aware formatting wastes tokens.

### Error shape

Every tool returns the standard `*ToolError` (`internal/mcp/errors.go`) on failure:

```json
{ "code": "upstream_unavailable", "message": "...", "retriable": true }
```

Codes used by this tool surface:

| Code | When |
|---|---|
| `invalid_argument` | Client-side validation rejected the input (bad magnet, mutually-exclusive fields, etc.) |
| `upstream_unavailable` | Network error, 5xx from qBittorrent, or context cancellation |
| `upstream_forbidden` | 401/403 from qBittorrent — loopback-auth-bypass is misconfigured |
| `upstream_not_found` | Hash, rule, or path that the request references does not exist |
| `internal` | Bug in qbit-mcp (e.g. response decode failure) |

### Normalized download state

| Normalized | Maps from raw qBittorrent states |
|---|---|
| `downloading` | `downloading`, `metaDL`, `forcedDL`, `allocating` |
| `seeding` | `uploading`, `forcedUP` |
| `paused` | `pausedDL`, `pausedUP`, `stoppedDL`, `stoppedUP` |
| `stalled` | `stalledDL`, `stalledUP` |
| `queued` | `queuedDL`, `queuedUP` |
| `checking` | `checkingDL`, `checkingUP`, `checkingResumeData`, `moving` |
| `errored` | `error`, `missingFiles` |
| `unknown` | `unknown` (or anything qBittorrent adds later) |

Every download in `list_downloads` output carries `state` as one of the normalized values above. qBittorrent's raw state string is not echoed back.

---

## Download tools

### `list_downloads`

Primary read. Filtered, sorted, paginated.

**Input:**

```json
{
  "states": ["downloading", "stalled"],   // optional; OR semantics
  "tags": ["tvdb:*", "weekly"],            // optional; OR; shell-style globs (path.Match)
  "hashes": ["aabbcc..."],                 // optional; exact set
  "sort": "added_on_desc",                 // see enum below; default added_on_desc
  "limit": 50,                             // default 50, max 200
  "offset": 0,                             // default 0
  "include_fields": ["save_path"]          // see opt-in fields below
}
```

`sort` enum: `name_asc`, `name_desc`, `added_on_asc`, `added_on_desc` (default), `size_asc`, `size_desc`, `progress_asc`, `progress_desc`, `dlspeed_desc`, `eta_asc`, `ratio_desc`.

`include_fields` opt-in values:

- **Field-level:** `save_path`, `content_path`, `download_path`, `magnet_uri`, `completion_on`, `last_activity`, `total_uploaded`, `total_downloaded`, `total_size`, `seeds`, `seeds_incomplete`, `peers`, `tracker_count`, `auto_tmm`, `sequential`, `force_start`, `super_seeding`, `first_last_piece_prio`, `ratio_limit`, `seeding_time`, `seeding_time_limit`, `private`.
- **Per-hash enrichments:** `trackers`, `files`. These trigger one additional upstream call per result, so they **require single-hash selection** — exactly one entry in `hashes`, no `states` filter, no `tags` filter. Anything else returns `invalid_argument` to prevent N+1 fan-out.
- **Convenience:** `"all"` expands to every field-level key (but not trackers/files). Use `["all", "trackers", "files"]` to get the kitchen-sink projection on a single hash.

Off by default.

**Output:**

```json
{
  "count": 12,
  "has_more": false,
  "downloads": [
    {
      "hash": "deadbeef...",
      "name": "[Erai-raws] Show - 03",
      "state": "downloading",
      "progress": 0.42,
      "size_bytes": 12345678,
      "dlspeed_bytes_per_sec": 524288,
      "upspeed_bytes_per_sec": 0,
      "eta_seconds": 1234,
      "ratio": 0.0,
      "tags": ["weekly"],
      "added_on": 1714851923
    }
  ]
}
```

`tags` is an array; `eta_seconds` is `-1` when unknown (qBittorrent's `8640000` sentinel collapses to `-1`).

### `add_download`

Add a single download by magnet URI.

**Input:**

```json
{
  "magnet": "magnet:?xt=urn:btih:deadbeef...&dn=Name&tr=udp://...",
  "tags": ["weekly"],             // optional; literal tag names, no commas
  "destination": "kura-inbox",    // optional; alias name only — see Destinations above
  "rename": "Custom name"         // optional; qBittorrent display-name override
}
```

Client-side validation rejects with `invalid_argument` when `magnet` is missing, has no `xt=urn:btih:<hash>` parameter, or the hash is not 40-char hex / 32-char base32; when `destination` is set to an unknown alias name; or when any tag contains a comma. Hash is parsed before the upstream call so the response carries it deterministically.

Magnet hash is normalized to 40-char lowercase hex in the response — base32 hashes are decoded to bytes and re-encoded as hex.

`auto_tmm` is always forced to `false` on the upstream call so the resolved destination is not silently overridden by qBittorrent's category-based routing. There is no input knob to change this — exposing one would defeat the destination-alias security boundary.

`paused`, `sequential`, and `auto_tmm` are not exposed as inputs. Magnets cannot fetch metadata while paused, sequential download is a power-user knob with no agent workflow, and auto_tmm would override the destination alias. If a workflow ever needs them, configure directly via the qBittorrent UI.

**Output:**

```json
{
  "hash": "deadbeef...",
  "accepted": true
}
```

`accepted: true` means qBittorrent acknowledged the add. Metadata fetch for new magnets is asynchronous in qbit; agents that need the resolved `name` should follow up with `list_downloads` filtered to the returned hash.

### `remove_downloads`

Remove downloads from qBittorrent's tracking. Pass exactly one of `hashes` or `filter`.

**Input:**

```json
{
  "hashes": ["aabbcc..."]
}
```

or:

```json
{
  "filter": { "states": ["downloading"], "tags": ["weekly"] }
}
```

Filter accepts `states` and `tags` (same semantics as `list_downloads`; tags use shell-style globs). Passing both `hashes` and `filter` returns `invalid_argument`. Passing neither also returns `invalid_argument` (refuses to operate on every download).

**There is no `delete_files` field** — files on disk are never deleted by this tool. The qBittorrent entry is removed; the underlying files are an operator concern (cron, kura's trash, manual rm). Exposing on-disk deletion would punch through the destination-alias security boundary.

**Output:**

```json
{
  "affected_count": 3,
  "affected_hashes": ["aabbcc...", "ddeeff...", "112233..."]
}
```

---

## Tag tools

### `list_tags`

Read all tags configured in qBittorrent.

**Output:**

```json
{
  "tags": ["weekly", "movies", "complete"]
}
```

Tags auto-create when `add_download.tags` references an unknown tag. No `create_tag` / `delete_tag` tools in v1.

---

## RSS tools

qBittorrent's RSS endpoints (`/api/v2/rss/*`) are not implemented by `github.com/autobrr/go-qbittorrent` as of v1.15.0. qbit-mcp reaches them via `client.GetHTTPClient()` and the configured `--qb-url`. The wrapper lives under `internal/qbtrss/` and is the only place in qbit-mcp that talks to qBittorrent without going through the SDK. Tracking issue: TODO file upstream.

Paths are flattened: `"Anime/Erai-raws/Erai-raws Releases"` is one feed inside two nested folders. Add/remove operations split the path internally; agents work with flat strings.

### `list_rss`

Feeds and folders. Items are omitted by default.

**Input:**

```json
{
  "include_items": false,   // default false; large feeds carry hundreds of items
  "since": "2026-05-01T00:00:00Z"   // optional; RFC3339; only items newer when include_items=true
}
```

**Output:**

```json
{
  "feeds": [
    {
      "path": "Anime/Erai-raws/Erai-raws Releases",
      "url": "https://example.com/rss",
      "has_error": false,
      "last_build_date": "2026-05-11T12:00:00Z",
      "item_count": 124,
      "items": [
        {
          "title": "[Erai-raws] Show - 03",
          "link": "magnet:?xt=urn:btih:...",
          "pub_date": "2026-05-11T12:00:00Z",
          "matching_rule": "anime-weekly"
        }
      ]
    }
  ]
}
```

`items` is omitted entirely when `include_items: false`. `matching_rule` is `null` when no rule matches that item.

### `add_rss_feed`

**Input:** `url`, `path`.

**Output:** `{ "ok": true }`.

### `remove_rss_item`

Remove a feed or folder. qBittorrent uses the same endpoint for both.

**Input:** `path`.

**Output:** `{ "ok": true }`.

### `list_rss_rules`

Auto-download rules.

**Output:**

```json
{
  "rules": [
    {
      "name": "anime-weekly",
      "enabled": true,
      "must_contain": "1080p",
      "must_not_contain": "VOSTFR",
      "use_regex": false,
      "episode_filter": "1x2;",
      "smart_filter": false,
      "affected_feeds": ["Anime/Erai-raws/Erai-raws Releases"],
      "save_path": "/data/anime",
      "ignore_days": 0,
      "add_paused": false
    }
  ]
}
```

### `set_rss_rule`

Create or replace a rule by name. qBittorrent's `setRule` is upsert semantics — same payload either way.

**Input:** `name`, plus any of the rule fields above (omitted fields keep their defaults on create / current values on edit). `affected_feeds` is an array of full flat feed paths. `destination` (alias name) replaces the raw `save_path` field for matched-download overrides.

**Output:** `{ "ok": true }`.

### `delete_rss_rule`

**Input:** `name`.

**Output:** `{ "ok": true }`.

---

## Deferred to follow-ups (not in v1)

- `update_downloads` — mid-life metadata edits (destination, tags, name). Dropped because everything is set at `add_download` time; metadata churn isn't an agent workflow. Re-add as one commit if a real need surfaces.
- `pause_downloads` / `resume_downloads` — operator concern (maintenance windows, bandwidth scheduling), not agent workflow. Re-add if a workflow surfaces.
- `get_download` — folded into `list_downloads` via `include_fields=["all", "trackers", "files"]` with single-hash selection.
- `recheck_torrents` — rare workflow; add when there is demand.
- `match_rss_articles` — preview which feed items a rule matches. Useful for rule debugging; agents rarely need it.
- `set_rss_settings` — global auto-download / processing toggles. Owner sets these in the qBittorrent UI.
- `set_torrent_speed_limits`, `set_torrent_share_limit` — agent-uncommon power-user knobs.
- `recheck`, `reannounce`, `set_force_start`, `set_super_seeding` — download-level toggles that complicate the v1 surface without a clear workflow story.
- Download file upload (raw bytes) — magnet URIs cover the agentic flow we ship dmhy-mcp + qbit-mcp for.
- Tracker / peer / file management (`add_trackers`, `ban_peers`, `set_file_priority`, `rename_file`).
- Search-plugin tools.

These all map cleanly onto the established `internalHandler` + `wrap` pattern; adding any one is one new struct pair plus one `mcpsdk.AddTool` call.

---

## Context-budget accounting

| Component | Approx tokens |
|---|---|
| Tool list (10 names + descriptions) loaded per turn | 0.8k – 1.0k |
| `list_downloads` default response, 50 downloads | 3.5k – 4.5k |
| `list_downloads` default response, 10 downloads | 0.7k – 1.0k |
| `list_downloads` single-hash with `include_fields=["all"]` (no trackers/files) | 0.3k |
| `list_downloads` single-hash with `include_fields=["all","trackers","files"]` on a typical anime release | 1.0k – 2.0k |
| `list_rss` without items | 0.2k per 10 feeds |
| `list_rss` with items, since=last week | scales with item count; cap by `since` |
| `list_rss_rules` | 0.1k per rule |

Rule of thumb: a download-aware agent that lists 20 active downloads and inspects one in detail eats ~2.5k tokens per probe loop. Comfortable budget at modern context sizes; would not be on smaller models.
