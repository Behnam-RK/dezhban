## Multi-Platform Standalone Network Kill Switch — Implementation Plan

Here's a structured plan for building a cross-platform tool. I'll break it into architecture, technology choices, and a phased build order.

---

### 1. Architecture Overview

The cleanest design separates concerns into three layers:

**Monitor layer** — polls your public IP and resolves its country. Platform-independent (pure Python/Go logic, just HTTP calls).

**Decision layer** — holds the blocklist, applies hysteresis (avoid flapping), and decides "block" or "allow." Also platform-independent.

**Enforcement layer** — the only platform-specific part. Translates a "block" / "unblock" decision into actual firewall commands per OS.

Keeping the enforcement layer behind a single interface (e.g. an abstract `FirewallBackend` class with `block()`, `unblock()`, `is_blocked()`) means ~90% of your code is shared and only one small module differs per platform.

---

### 2. Technology Choice

| Option | Pros | Cons |
|---|---|---|
| **Python** | Fast to write, great HTTP libs, easy cross-platform | Needs a runtime; packaging into a true standalone binary requires PyInstaller |
| **Go** | Single static binary per platform, easy concurrency, no runtime | More verbose firewall shelling |
| **Rust** | Tiny robust binaries, strong safety | Steeper curve, slower to prototype |

For a "standalone" deliverable, **Go** is the sweet spot — `go build` per OS gives you one self-contained executable with no dependencies. Python is fine for a prototype if you'll wrap it with PyInstaller later.

---

### 3. Enforcement Layer Per Platform

- **Linux:** `nftables` (preferred) or `iptables`. Create a dedicated table/chain so you can flush only your rules without disturbing others. Drop all `output`/`input` except the allowlist.
- **macOS:** `pfctl` with an anchor file. Load a custom anchor so your rules sit isolated from the system ruleset.
- **Windows:** Windows Filtering Platform via PowerShell (`New-NetFirewallRule`) or `netsh advfirewall`. Group rules under a named profile for clean teardown.

Each backend implements the same three operations and uses a **unique tag/name** for its rules so cleanup is surgical.

---

### 4. Critical Design Decisions to Lock Early

- **Fail mode:** If the geolocation API is unreachable, do you block (fail-closed, safer) or allow (fail-open, more usable)? For a security tool, fail-closed is the typical default.
- **Allowlist:** The IP-check endpoints, DNS, and loopback **must** stay reachable even while blocking — otherwise recovery detection breaks. Use multiple geolocation providers for redundancy.
- **Hysteresis:** Require N consecutive readings before toggling to avoid flapping on a single bad lookup.
- **Detection method:** API-based (simple, but rate-limited and spoofable) vs. country IP-range lists (offline, more robust). Consider a hybrid.

---

### 5. Phased Build Order

1. **Prototype the monitor** — script that prints your current country on a loop. No firewall yet.
2. **Build one enforcement backend** (your own OS) with a manual `block`/`unblock` CLI to verify the firewall calls work and tear down cleanly.
3. **Wire monitor → decision → enforcement** on that one platform end-to-end.
4. **Add the abstraction layer** and port enforcement to the other two OSes.
5. **Add resilience** — fail mode, hysteresis, allowlist, multi-provider lookups.
6. **Add persistence** — run as `systemd` service (Linux), `launchd` daemon (macOS), Windows Service.
7. **Add safety nets** — clean shutdown that always removes rules, a status command, logging, and a manual override.
8. **Package** — `go build` per target, or PyInstaller bundles.

---

### 6. Don't-Forget Safety Nets

- A **panic/manual unblock** command that works even if the daemon crashes — a stale block-all rule could lock you out of your own network.
- **Idempotent** rule application (applying "block" twice shouldn't stack duplicate rules).
- **Privilege check** at startup with a clear error if not run as root/admin.

---

Want me to start on **Phase 1** (the platform-independent monitor), or scaffold the full project structure with the abstraction layer first? Let me know your target language and primary OS.
