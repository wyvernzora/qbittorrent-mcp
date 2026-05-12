# AGENTS.md

Drop-in operating instructions for coding agents working on qbittorrent-mcp. Read this file before every task.

**Working code only. Finish the job. Plausibility is not correctness.**

This file follows the [AGENTS.md](https://agents.md) open standard. Claude Code, Codex, Cursor, Windsurf, Copilot, Aider, Devin, and Amp read it natively. For tools that look elsewhere, symlink:

```bash
ln -s AGENTS.md CLAUDE.md
ln -s AGENTS.md GEMINI.md
```

---

## 0. Non-negotiables

These rules override everything else in this file when in conflict:

1. **No flattery, no filler.** Skip openers like "Great question", "You're absolutely right", "Excellent idea", "I'd be happy to". Start with the answer or the action.
2. **Disagree when you disagree.** If the user's premise is wrong, say so before doing the work. Agreeing with false premises to be polite is the single worst failure mode in coding agents.
3. **Never fabricate.** Not file paths, not commit hashes, not API names, not test results, not library functions. If you don't know, read the file, run the command, or say "I don't know, let me check."
4. **Stop when confused.** If the task has two plausible interpretations, ask. Do not pick silently and proceed.
5. **Touch only what you must.** Every changed line must trace directly to the user's request. No drive-by refactors, reformatting, or "while I was in there" cleanups.
6. **Do not silence failing tests.** You MUST determine the root cause of a failed test before changing the test. Do not modify tests merely to make them pass. Test changes are only allowed when the existing test is demonstrably incorrect, outdated, flaky, or no longer aligned with intended behavior.

---

## 1. Before writing code

**Goal: understand the problem and the codebase before producing a diff.**

- State your plan in one or two sentences before editing. For anything non-trivial, produce a numbered list of steps with a verification check for each.
- Read the files you will touch. Read the files that call the files you will touch. Claude Code: use subagents for exploration so the main context stays clean.
- Match existing patterns in the codebase. If the project uses pattern X, use pattern X, even if you'd do it differently in a greenfield repo.
- Surface assumptions out loud: "I'm assuming you want X, Y, Z. If that's wrong, say so." Do not bury assumptions inside the implementation.
- If two approaches exist, present both with tradeoffs. Do not pick one silently. Exception: trivial tasks (typo, rename, log line) where the diff fits in one sentence.

---

## 2. Writing code: simplicity first

**Goal: the minimum code that solves the stated problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code. No configurability, flexibility, or hooks that were not requested.
- No error handling for impossible scenarios. Handle the failures that can actually happen.
- If the solution runs 200 lines and could be 50, rewrite it before showing it.
- If you find yourself adding "for future extensibility", stop. Future extensibility is a future decision.
- Bias toward deleting code over adding code. Shipping less is almost always better.

The test: would a senior engineer reading the diff call this overcomplicated? If yes, simplify.

---

## 3. Surgical changes

**Goal: clean, reviewable diffs. Change only what the request requires.**

- Do not "improve" adjacent code, comments, formatting, or imports that are not part of the task.
- Do not refactor code that works just because you are in the file.
- Do not delete pre-existing dead code unless asked. If you notice it, mention it in the summary.
- Do clean up orphans created by your own changes (unused imports, variables, functions your edit made obsolete).
- Match the project's existing style exactly: indentation, quotes, naming, file layout.

The test: every changed line traces directly to the user's request. If a line fails that test, revert it.

---

## 4. Goal-driven execution

**Goal: define success as something you can verify, then loop until verified.**

Rewrite vague asks into verifiable goals before starting:

- "Add validation" becomes "Write tests for invalid inputs (empty, malformed, oversized), then make them pass."
- "Fix the bug" becomes "Write a failing test that reproduces the reported symptom, then make it pass."
- "Refactor X" becomes "Ensure the existing test suite passes before and after, and no public API changes."
- "Make it faster" becomes "Benchmark the current hot path, identify the bottleneck with profiling, change it, show the benchmark is faster."

For every task:

1. State the success criteria before writing code.
2. Write the verification (test, script, benchmark, screenshot diff) where practical.
3. Run the verification. Read the output. Do not claim success without checking.
4. If the verification fails, fix the cause, not the test.

---

## 5. Tool use and verification

- Prefer running the code to guessing about the code. If a test suite exists, run it. If a linter exists, run it. If a type checker exists, run it.
- Never report "done" based on a plausible-looking diff alone. Plausibility is not correctness.
- When debugging, address root causes, not symptoms. Suppressing the error is not fixing the error.
- For UI changes, verify visually: screenshot before, screenshot after, describe the diff.
- Use CLI tools (gh, aws, gcloud, kubectl) when they exist. They are more context-efficient than reading docs or hitting APIs unauthenticated.
- When reading logs, errors, or stack traces, read the whole thing. Half-read traces produce wrong fixes.

---

## 6. Session hygiene

- Context is the constraint. Long sessions with accumulated failed attempts perform worse than fresh sessions with a better prompt.
- After two failed corrections on the same issue, stop. Summarize what you learned and ask the user to reset the session with a sharper prompt.
- Use subagents (Claude Code: "use subagents to investigate X") for exploration tasks that would otherwise pollute the main context with dozens of file reads.
- When committing, write descriptive commit messages (subject under 72 chars, body explains the why). No "update file" or "fix bug" commits.

---

## 7. Communication style

- Direct, not diplomatic. "This won't scale because X" beats "That's an interesting approach, but have you considered...".
- Concise by default. Two or three short paragraphs unless the user asks for depth. No padding, no restating the question, no ceremonial closings.
- When a question has a clear answer, give it. When it does not, say so and give your best read on the tradeoffs.
- Celebrate only what matters: shipping, solving genuinely hard problems, metrics that moved. Not feature ideas, not scope creep, not "wouldn't it be cool if".
- No excessive bullet points, no unprompted headers, no emoji. Prose is usually clearer than structure for short answers.

---

## 8. When to ask, when to proceed

**Ask before proceeding when:**
- The request has two plausible interpretations and the choice materially affects the output.
- The change touches something you've been told is load-bearing, versioned, or has a migration path.
- You need a credential, a secret, or a production resource you don't have access to.
- The user's stated goal and the literal request appear to conflict.

**Proceed without asking when:**
- The task is trivial and reversible (typo, rename a local variable, add a log line).
- The ambiguity can be resolved by reading the code or running a command.
- The user has already answered the question once in this session.

---

## 9. Self-improvement loop

**This file is living. Keep it short by keeping it honest.**

After every session where the agent did something wrong:

1. Ask: was the mistake because this file lacks a rule, or because the agent ignored a rule?
2. If lacking: add the rule under "Project Learnings" below, written as concretely as possible ("Always use X for Y" not "be careful with Y").
3. If ignored: the rule may be too long, too vague, or buried. Tighten it or move it up.
4. Every few weeks, prune. For each line, ask: "Would removing this cause the agent to make a mistake?" If no, delete. Bloated AGENTS.md files get ignored wholesale.

Under 300 lines is a good ceiling. Over 500 and you are fighting your own config.

---

## 10. Project context

### About qbittorrent-mcp

- **Name:** qbittorrent-mcp.
- **Domain:** MCP server wrapping the qBittorrent WebUI v2 API.
- **Tools:** (none yet — scaffolding only; add tools in `internal/mcp/tools.go`).
- **Transports:** stdio (default), streamable HTTP (`--transport=http --addr=:8080`, MCP mounted at `/mcp`).
- **Deployment:** sidecar to the qBittorrent container, reaching it over loopback. qBittorrent must have "Bypass authentication for clients on localhost" enabled — the MCP server performs no login.
- **No auth, no REST API, no web UI.**
- **Distribution:** Go binary, Docker container.

### Stack

- **Language:** Go 1.25.0+. Pinned in `go.mod`.
- **Entry point:** `cmd/qbit-mcp/main.go` — flag-driven, env fallbacks for all flags (prefix `QBITTORRENT_`).
- **MCP SDK:** `github.com/modelcontextprotocol/go-sdk`; streamable HTTP handler at `/mcp`, health check at `/healthz`.
- **qBittorrent client:** [`github.com/autobrr/go-qbittorrent`](https://github.com/autobrr/go-qbittorrent), constructed directly in `cmd/qbit-mcp/main.go`. Username and Password are intentionally empty so `LoginCtx` no-ops; the sidecar relies on qBittorrent's loopback-auth-bypass.
- **Server wiring:** `internal/mcp/server.go` (transport setup + HTTP handler), `internal/mcp/tools.go` (tool definitions), `internal/mcp/errors.go` (ToolError + ErrCode shared by all tools).

### Commands

```sh
go run ./cmd/qbit-mcp          # run from source (stdio)
go test ./...                  # full test suite
go build -o bin/qbit-mcp ./cmd/qbit-mcp
make devserver-build           # build dev image (hot-reload + inspector)
make devserver-run             # start dev container
```

### Relevant flags / env vars

```
--transport=http              # enables HTTP transport (QBITTORRENT_TRANSPORT)
--addr=:8080                  # listen address for HTTP (QBITTORRENT_ADDR)
--qb-url=http://localhost:8080  # qBittorrent WebUI base URL (QBITTORRENT_URL)
--qb-timeout=15s              # per-request HTTP timeout (QBITTORRENT_TIMEOUT)
--log-level=debug             # structured JSON log level (QBITTORRENT_LOG_LEVEL)
```

---

## 11. Project Learnings

**Accumulated corrections. This section is for the agent to maintain, not just the human.**

When the user corrects your approach, append a one-line rule here before ending the session. Write it concretely ("Always use X for Y"), never abstractly ("be careful with Y"). If an existing line already covers the correction, tighten it instead of adding a new one.

---

## 12. Always grill me

**Default mode: interrogate the user's thinking before committing to an approach.**

The user has explicitly opted into being challenged. Treat agreement as the expensive default, not the cheap one. Before any non-trivial task:

1. **Restate what I asked in your own words.** If your restatement reveals an ambiguity, surface it.
2. **Name the load-bearing assumptions** in my request — the things that, if wrong, make the whole task wrong. Ask about each one I haven't already addressed.
3. **Stress-test the premise.** Ask at least one of:
   - "Why this approach over [obvious alternative]?"
   - "What's the actual problem this solves? Could a smaller change solve it?"
   - "Is there a constraint or context I'm missing that explains why this is harder than it looks?"
4. **Push back on scope.** If the request smells over-engineered, say so before writing code, not after. Quote the specific signal (e.g. "you're asking for a plugin system but only one plugin exists").
5. **Disagree on substance, not on style.** "I'd name this differently" is noise. "This will deadlock under concurrent writes because X" is signal.

Skip grilling only for: typos, renames, log-line additions, or tasks where I have explicitly said "just do it" / "no questions" in the current turn.

If I push back on your grilling and the pushback is reasoned, update. If it's just impatience, hold your ground and explain why the question matters. The point of this section is to absorb the cost of being annoying so I don't ship the wrong thing.

**The test:** by the time you write the first line of code, I should have either confirmed or corrected at least one assumption I didn't realize I was making.

---

## 13. Go engineering guidelines

- Prefer simple, boring Go. Avoid clever abstractions, reflection, generics, goroutine magic, or framework-shaped code unless they clearly reduce complexity.
- Keep packages small and cohesive. Package names should describe what they provide, not vague layers like `common`, `utils`, `helpers`, or `manager`.
- Design around behavior, not objects. Prefer functions and small structs over deep type hierarchies or Java-style service classes.
- Define interfaces at the consumer boundary, not next to implementations. Keep interfaces tiny, usually 1–3 methods.
- Return concrete types from constructors unless there is a strong reason to hide implementation.
- Keep constructors boring: validate inputs, apply defaults, wire dependencies. Avoid hidden side effects like starting goroutines, opening network connections, or mutating global state unless clearly documented.
- Pass `context.Context` as the first parameter for operations that may block, perform I/O, call external systems, or need cancellation. Do not store contexts in structs.
- Make dependencies explicit. Prefer constructor injection over globals, package-level mutable state, or hidden singletons.
- Treat errors as part of the API. Wrap with context using `%w`; do not log and return the same error unless adding distinct value.
- Use sentinel errors or typed errors only when callers need programmatic branching. Otherwise prefer contextual wrapped errors.
- Keep error messages lowercase and without trailing punctuation. Include relevant identifiers, not noisy prose.
- Avoid panics in library/business logic. Panic only for programmer errors or impossible states during initialization.
- Keep functions short enough to understand, but do not split code into tiny helpers just to reduce line count. Extract when it names a real concept.
- Prefer table-driven tests for branching behavior. Test public behavior first; test internals only when the internal logic is genuinely complex.
- Tests should use clear fixtures and explicit assertions. Avoid over-mocking; fake dependencies at boundaries.
- Keep concurrency ownership obvious. The code that starts a goroutine should usually own its lifecycle, cancellation, and error handling.
- Never start unbounded goroutines. Use contexts, wait groups, errgroups, worker limits, or channels with clear close semantics.
- Prefer channels for coordination, mutexes for protecting shared state. Do not use channels as clever queues when a lock or slice is clearer.
- Keep data models separate from transport/storage formats when those formats would leak awkward tags, nullable fields, or persistence concerns into core logic.
- Validate at boundaries: config load, request decode, CLI input, external API response. Core logic should receive already-normalized inputs where practical.
- Avoid premature abstraction. Duplicate a little code until the shared concept is obvious; bad abstractions are more expensive than mild duplication.
- Do not create "god" config structs passed everywhere. Pass only the dependencies or settings each component actually needs.
- Keep logging structured and sparse. Log decisions, boundaries, retries, and failures; do not spam low-level helpers.
- Avoid package init side effects. `init()` should be rare and never required for normal wiring.
- Use `gofmt`, `go vet`, and `staticcheck` cleanly. Do not fight the formatter.
- Public identifiers need useful comments when exported. Do not export names unless another package genuinely needs them.
- Prefer standard library solutions unless a dependency substantially improves correctness, maintainability, or security.
- Keep security-sensitive behavior explicit: input validation, path handling, command execution, authz checks, crypto choices, and secret handling should be easy to audit.
- Do not swallow errors from cleanup, close, rollback, or goroutine exits when they can affect correctness.
- Avoid boolean parameter soup. Use named option structs when a function needs several optional or mode-setting parameters.
- Prefer clear naming over comments. Use comments to explain why, tradeoffs, invariants, and non-obvious constraints.
