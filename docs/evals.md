# Agent eval set (skeleton)

Not yet implemented — this document is the **spec** for the eval workstream. Build the harness against this contract; do not freeze the tasks themselves until at least one round of `make devserver-run` + MCP inspector shakes out the obvious bugs (see `docs/tools.md` § "Live smoke test" deferral note).

## Why evals

Anthropic's [Writing tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents) is explicit: tool quality is iterated empirically. Unit tests catch handler regressions; only end-to-end runs catch description-clarity issues, schema misreads, and friction in tool-call sequencing. The audit findings in `docs/tools.md` were derived from public best practices, not from observing this server in use — so until a real agent has driven the surface, every tool description is plausibility, not correctness.

## Harness shape

Three components.

1. **A test qBittorrent instance** with known initial state. Probably a docker-compose: `qbittorrent:latest` + `qbit-mcp` + the eval runner.
2. **An eval runner** that:
   - Spins up qbit-mcp pointed at the test qBittorrent.
   - For each task in `tasks/`: starts a fresh Claude session, sends the task prompt, captures the full transcript (tool calls + token counts + final response).
   - Asserts the trace against expected tool-call sequence and final-state expectations.
3. **A scoring report** that emits, per task: pass/fail, total tokens, tool-call count, wall-clock duration, error rate.

The runner does not need to be sophisticated. A Go binary using `github.com/anthropics/anthropic-sdk-go` (or the API directly) is enough — see how Anthropic's internal `mcp-eval` tool is structured if access is available.

## Task taxonomy

Five categories. Each task lives in `evals/tasks/<id>.json` (or .yaml — pick one and stick to it).

### A. Single-tool happy path

Smoke that the surface works at all.

- **A1 — list-the-thing.** "Show me what I'm currently downloading." Expected: `qbit_search_downloads` with no args, lean projection.
- **A2 — list tags.** "What tags do I have configured?" Expected: `qbit_list_tags`.
- **A3 — list destinations.** "Where can I save downloads?" Expected: `qbit_list_destinations`.

### B. Multi-step workflow

The interesting category. Catches description-clarity bugs and ordering misreads.

- **B1 — subscribe to a series.** "Subscribe to https://dmhy.example/rss?id=12345 with tag tvdb:12345, save to kura-inbox." Expected: `qbit_subscribe` (no need to discover destination first — it's in the prompt). Failure mode: agent calls `qbit_list_destinations` first, wasting a tool call.
- **B2 — subscribe with discovery.** "Subscribe to <url> and put it in whatever destination is for kura." Expected: `qbit_list_destinations` then `qbit_subscribe`. Failure mode: agent makes up `"kura"` or `"kura-inbox"` without checking.
- **B3 — prune stalled.** "Remove every download that's been stalled for a while." Expected: `qbit_search_downloads` with `states=["stalled"]`, then `qbit_remove_downloads` with the resulting hash list. Failure mode: agent uses `filter` selector instead of explicit `hashes` (acceptable, but token-wasteful if the count is small).
- **B4 — reverse-resolve.** Given a download with `save_path=/mnt/kura`, "What destination alias is this?" Expected: `qbit_list_destinations`. Failure mode: agent fabricates an alias name from the path.

### C. Idempotency / retry

- **C1 — duplicate add.** "Add this magnet" (twice in sequence). Expected: both calls return `accepted=true`; second carries `already_existed=true`. Failure mode: agent pre-checks via `qbit_search_downloads` instead of just calling `qbit_add_download` again (the idempotency is in the description; if the agent doesn't trust it, the description is unclear).
- **C2 — upsert subscription.** Subscribe with one tag set; then `qbit_subscribe` again with same name + same `feed_url` + different tags. Expected: rule replaced, no error. Failure mode: agent calls `qbit_unsubscribe` first.

### D. Error-path correctness

- **D1 — upstream_forbidden.** Test qBittorrent has loopback-auth-bypass **off**. Expected: agent surfaces the operator-action message to the user; does NOT retry. Failure mode: agent retries indefinitely.
- **D2 — invalid_argument on tag with comma.** "Add this magnet with tags ['weekly,active']." Expected: agent surfaces the validation message and asks the user to clarify (`weekly` and `active` as separate tags? or a literal `weekly,active`?). Failure mode: agent silently strips the comma.
- **D3 — feed_url change rejected.** Subscribe with `feed_url=A`, then `qbit_subscribe` with same name + `feed_url=B`. Expected: agent calls `qbit_unsubscribe` then `qbit_subscribe` with B (per the description's "unsubscribe and resubscribe" guidance). Failure mode: agent retries with A.

### E. Filter + pagination

Catches the new R1 affordances on `qbit_search_subscriptions`.

- **E1 — name-glob filter.** With 50 subscriptions, "Show me my kura-* subscriptions." Expected: `qbit_search_subscriptions` with `name_glob="kura-*"`. Failure mode: fetch all + filter client-side (token waste).
- **E2 — pagination.** With 250 subscriptions, "List all my subscriptions." Expected: agent paginates via `offset` until `has_more=false`. Failure mode: assumes the first page is everything.

## Metrics

Per Anthropic's guidance, track per task:

| Metric | What |
| --- | --- |
| **Pass/fail** | Final state matches the assertion. |
| **Tool calls** | Total invocations. Lower is better when the task is simple. |
| **Wasted calls** | Calls the spec marks unnecessary (e.g., a pre-check before an idempotent `qbit_add_download`). |
| **Tokens (in/out)** | Input tokens (mostly tool descriptions + transcript) and output tokens. |
| **Wall-clock** | End-to-end duration. |
| **Error rate** | Fraction of attempts that fail across N seeds (try ≥3 seeds per task). |

Read the transcripts. Numbers are necessary but not sufficient — the qualitative read on _why_ the agent picked tool X over Y is where description-clarity bugs surface.

## What "good" looks like

Initial targets — adjust once a baseline exists:

- Pass rate ≥ 95% across all tasks at temperature=0.
- Median tool-call count for A1 = 1; for B1 = 1; for B3 = 2.
- Zero `Wasted calls` on category C (the idempotency contract is the whole point).
- Description-clarity bugs surface only on B and D — A and C should be boring.

## Iteration loop

1. Run the eval set.
2. Sort failures by category; read transcripts for the worst category first.
3. Identify the description sentence (or schema field) the agent misread.
4. Edit. Re-run. Compare.
5. Commit description changes separately from handler changes so the eval delta is interpretable.

## Out of scope (for v1 of this eval)

- Multi-server interaction (qbit-mcp + dmhy-mcp in the same session). Save for a cross-server eval suite once both surfaces stabilize.
- Long-running tasks (subscribe → wait for match → confirm download added). Needs a clock-fast-forward shim on qBittorrent that doesn't exist.
- Adversarial prompts (prompt injection through feed titles). Important but a different workstream — security review, not surface design.
