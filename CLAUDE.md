# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

**dezhban** (Persian: "gatekeeper") is a standalone, cross-platform **network
kill switch** written in Go. It polls the machine's public IP, resolves its
country, and when the country matches a blocklist it drives the OS firewall to
cut traffic — keeping a minimal allowlist so recovery detection keeps working.

Status: built phase-by-phase. See `docs/plans/` — `README.md` is the index;
each `phase-N-*.md` is an independently buildable unit with its own acceptance
checks. Implement and verify one phase before starting the next.

## Commands

```bash
go build ./...                      # build everything
go vet ./...                        # static checks
go test ./...                       # all tests
go test ./internal/config -run TestLoad   # a single package / test
go run ./cmd/dezhban status         # run a subcommand without installing

# cross-compile a single target
GOOS=linux GOARCH=amd64 go build ./cmd/dezhban
make build-all                      # all 5 targets into dist/, version-stamped
```

The binary's subcommands: `run`, `block`, `unblock`, `status`, `panic`,
`install`, `uninstall`, `start`, `stop`, `detect-vpn`, `version`. Privileged
commands (`run`, `block`, `unblock`, `panic`, `install`, `uninstall`, `start`,
`stop`) require root/admin and print a clear error otherwise.

## Architecture — three layers

```
Monitor    internal/monitor    polls public IP, resolves country   (platform-independent)
Decision   internal/decision   blocklist + hysteresis + fail-mode → Block/Allow  (platform-independent)
Firewall   internal/firewall   FirewallBackend per OS              (ONLY platform-specific part)
```

The **`FirewallBackend` interface** (`internal/firewall/backend.go`) is the seam
that keeps ~90% of the code shared. Rules per OS:

Every backend shells out to the OS's own firewall tooling (no netlink/WFP
libraries are linked) and tags rules with the unique name `dezhban`:

- **macOS** → shell out to `pfctl`, dedicated `dezhban` anchor (`pf_darwin.go`)
- **Linux** → shell out to `nft`, dedicated `dezhban` table (`nft_linux.go`)
- **Windows** → shell out to `netsh`/PowerShell (WFP), tagged sublayer (`wfp_windows.go`)

Backends are selected by **build tags** (`//go:build darwin|linux|windows`), so
each target compiles only its own backend.

### Rules that must not be broken

- **Never call `pfctl`/`nft`/WFP directly from `run` or `cmd/`** — go through
  `FirewallBackend`. The whole design depends on that seam.
- Every firewall rule carries the unique tag/anchor/table name **`dezhban`** so
  teardown (`Unblock`/`Cleanup`) is surgical and never touches unrelated rules.
- `Block` must be **idempotent** (re-block must not stack duplicate rules).
- `Cleanup()` must always be safe to call and is wired to run on shutdown
  (`defer` + `signal.NotifyContext`). A stale block-all rule can lock the user
  out of their own network — `panic` (Phase 7) removes rules even with no daemon.
- Default to **fail-closed**: when the country can't be determined, block. But
  the allowlist (loopback + DNS + geo-API egress) must stay open so recovery
  detection still works, or the machine can lock itself out.

## Conventions

- **Dependencies are deliberate.** Stdlib for CLI (`flag`), config (JSON),
  logging (`log/slog`), HTTP, and firewall control (shell out to the OS tooling).
  The **only** third-party dep is `kardianos/service` (cross-platform service
  manager, Phase 6). The Linux/Windows backends shell out to `nft` and
  `netsh`/PowerShell rather than linking `google/nftables` / `tailscale/wf` — one
  consistent shell-out model, zero extra deps. Don't add `cobra`/`viper`/etc. —
  the deliverable is a dependency-light standalone binary.
- Config is JSON with string durations (e.g. `"30s"`); on-disk shape is the
  `fileConfig` DTO in `internal/config`, converted to a validated `Config`.
- Module path `github.com/behnam-rk/dezhban` (adjust if the repo moves).


## Code Exploration Policy

Always use jCodemunch-MCP tools for code navigation. Never fall back to Read, Grep, Glob, or Bash for code exploration.
**Exception:** Use `Read` when you need to edit a file — the agent harness requires a `Read` before `Edit`/`Write` will succeed. Use jCodemunch tools to *find and understand* code, then `Read` only the specific file you're about to modify.

**Start any session:**
1. `resolve_repo { "path": "." }` — confirm the project is indexed. If not: `index_folder { "path": "." }`
2. `suggest_queries` — when the repo is unfamiliar

**Finding code:**
- symbol by name → `search_symbols` (add `kind=`, `language=`, `file_pattern=`, `decorator=` to narrow)
- decorator-aware queries → `search_symbols(decorator="X")` to find symbols with a specific decorator (e.g. `@property`, `@route`); combine with set-difference to find symbols *lacking* a decorator (e.g. "which endpoints lack CSRF protection?")
- string, comment, config value → `search_text` (supports regex, `context_lines`)
- database columns (dbt/SQLMesh) → `search_columns`

**Reading code:**
- before opening any file → `get_file_outline` first
- one or more symbols → `get_symbol_source` (single ID → flat object; array → batch)
- symbol + its imports → `get_context_bundle`
- specific line range only → `get_file_content` (last resort)

**Repo structure:**
- `get_repo_outline` → dirs, languages, symbol counts
- `get_file_tree` → file layout, filter with `path_prefix`

**Relationships & impact:**
- what imports this file → `find_importers`
- where is this name used → `find_references`
- is this identifier used anywhere → `check_references`
- file dependency graph → `get_dependency_graph`
- what breaks if I change X → `get_blast_radius`
- what symbols actually changed since last commit → `get_changed_symbols`
- find unreachable/dead code → `find_dead_code`
- class hierarchy → `get_class_hierarchy`

## Session-Aware Routing

**Opening move for any task:**
1. `plan_turn { "repo": "...", "query": "your task description", "model": "<your-model-id>" }` — get confidence + recommended files; the `model` parameter narrows the exposed tool list to match your capabilities at zero extra requests.
2. Obey the confidence level:
   - `high` → go directly to recommended symbols, max 2 supplementary reads
   - `medium` → explore recommended files, max 5 supplementary reads
   - `low` → the feature likely doesn't exist. Report the gap to the user. Do NOT search further hoping to find it.

**Interpreting search results:**
- If `search_symbols` returns `negative_evidence` with `verdict: "no_implementation_found"`:
  - Do NOT re-search with different terms hoping to find it
  - Do NOT assume a related file (e.g. auth middleware) implements the missing feature (e.g. CSRF)
  - DO report: "No existing implementation found for X. This would need to be created."
  - DO check `related_existing` files — they show what's nearby, not what exists
- If `verdict: "low_confidence_matches"`: examine the matches critically before assuming they implement the feature

**After editing files:**
- If PostToolUse hooks are installed (Claude Code only), edited files are auto-reindexed
- Otherwise, call `register_edit` with edited file paths to invalidate caches and keep the index fresh
- For bulk edits (5+ files), always use `register_edit` with all paths to batch-invalidate

**Token efficiency:**
- If `_meta` contains `budget_warning`: stop exploring and work with what you have
- If `auto_compacted: true` appears: results were automatically compressed due to turn budget
- Use `get_session_context` to check what you've already read — avoid re-reading the same files

## Model-Driven Tool Tiering

Your jcodemunch-mcp server narrows the exposed tool list based on the model you are running as. To avoid wasting requests on primitives when a composite would do, always include `model="<your-model-id>"` in your opening `plan_turn` call.

Replace `<your-model-id>` with your active model:
- Claude Opus variants → `claude-opus-4-7` (or any `claude-opus-*`)
- Claude Sonnet variants → `claude-sonnet-4-6`
- Claude Haiku variants → `claude-haiku-4-5`
- GPT-4o / GPT-5 / o1 / Llama → use the model id as printed by your runner

The `model=` parameter rides on the existing `plan_turn` call — it does **not** add a separate tool invocation. If `plan_turn` is not appropriate for a non-code task, call `announce_model(model="...")` once instead.


## Doc Exploration Policy

Always use jDocMunch-MCP tools for documentation navigation. Never fall back to Read for doc exploration.
**Exception:** Use `Read` when you need exact line numbers for `Edit`.

**Start any session:**
1. `doc_list_repos` — check what's indexed. If your docs aren't there: `index_local { "path": "." }`

**Finding content:**
- keyword/topic search -> `search_sections` (returns summaries only)
- browse structure -> `get_toc` (flat) or `get_toc_tree` (nested)
- single document -> `get_document_outline`

**Reading content:**
- one section -> `get_section` (full content via byte-range)
- multiple sections -> `get_sections` (batch)
- section + context -> `get_section_context` (ancestors + children)

**Maintenance:**
- broken internal links -> `get_broken_links`
- code/doc coverage gap -> `get_doc_coverage`
