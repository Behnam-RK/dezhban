# ADR-0016: A bundle identifier is redacted by the key that names it

**Date**: 2026-09-13
**Status**: accepted, implemented
**Deciders**: Behnam RK

## Context

`dezhban report` writes a diagnostic bundle meant to be pasted into a public
issue, so `internal/redact` must replace every identifier in it with a stable
placeholder. It has two ways to find one, and they are not peers.

The **key-aware** pass parses a JSON document and rewrites a value because of
the key above it: a string under `endpoint`, `tunnelInterfaces` or
`activeProfile` is an identifier by definition, whatever it happens to spell.
The **literal-name replay** (`knownNames`) is the fallback: it takes the names
already replaced elsewhere and substitutes them wherever they appear as words in
free text. It is what reaches a name no shape and no key can see — a
single-label host inside a resolver's error, an interface name in a rendered pf
rule.

The replay is also where every defect in this package has come from. Across the
review loop on #55 it claimed the posture and mode strings when a profile shared
their name, double-minted host-shaped names under two kinds, mangled doctor's
fix commands, and needed a reserved-word list to stop claiming dezhban's own
vocabulary. Issue #63 was left open for exactly that reason, with the note that
the cleaner fix was probably "at the source" — stop interpolating identifiers
into prose at all.

Implementing it showed the source-side fix cannot stand alone. The entries that
actually leaked were `rules-preview.txt` and `log.txt`: a rendered pf/nft
ruleset and slog records. A pf rule carries an interface as `pass out quick on
{ nordlynx }`, because that is what a pf rule *is*, and an OS error string is
written by the OS. Neither can be restructured into fields by us.

## Decision

An identifier in a bundle is redacted **by the key that names it**. Where we
author the data — `doctor.json`, `state.json` — the identifier is carried as a
field under a key the redactor's value switch already knows, and the prose that
mentions it is composed by each renderer at display time. The literal-name
replay stays, is widened to cover interface names, and is the **fallback** for
text we do not author: rendered rulesets, log records, OS error strings.

## Alternatives considered

### Alternative 1: widen the replay and leave the prose alone

- **Pros**: one small change; no struct, renderer or Swift changes; closes every
  observed leak on its own.
- **Cons**: makes the pass with this package's worst defect history the primary
  mechanism for the whole bundle, including entries whose prose we control and
  could simply have kept the identifier out of. Every word dezhban writes becomes
  a word the replay might claim, and the only defence is a hand-maintained
  reserved list that has needed extending in every round it has existed.
- **Why not**: it is the shape that keeps failing, applied to more surface.

### Alternative 2: remove identifiers from prose entirely, no replay

- **Pros**: kills the class; nothing to replay because nothing repeats a name.
- **Why not**: impossible for the entries that actually leak. `rules-preview.txt`
  is firewall syntax, `log.txt` is slog output, and `render.Display`'s detail
  quotes `EnforcementErr` and `LookupErr` verbatim from pfctl, nft and the
  resolver. Adopting this alone would have closed #63 in the two files that were
  already covered and in neither of the two that were not.

### Alternative 3: retype `doctorCheck.Details` as a template plus fields, with no composed line

- **Pros**: the identifier is unambiguously data.
- **Why not**: `Details` is documented as the lines both renderers print, and the
  macOS app decodes them. A renderer that cannot compose the sentence shows a
  line with a hole. Keeping a `line()` on both sides — Go and Swift, mirrored —
  gets the field without giving up the contract.

## Consequences

### Positive

- An identifier we author is recognised for what it is, whatever it spells, and
  cannot be missed because it lacked a dot or a boundary character.
- The replay's blast radius shrinks to text we do not write, where over-claiming
  a word is noise rather than a rewritten diagnosis.
- One rule for a reader: if a value is an identity, it has a key. `doctor --json`
  and `status --json` are more useful to the app for the same reason.

### Negative

- `doctor.json`'s `details` became objects, a coordinated Go and Swift change.
  The two ship in one `.pkg`, so the skew window is the dev loop, not a release.
- Two places now compose the same sentence (`doctorDetail.line` in Go,
  `DoctorDetail.line` in Swift). They must stay in step; both are tested against
  the same expected string.

### Risks

- **The replay still has to be right for what it now covers.** Mitigated by
  refusing any value that is spelled like a minted token (which is how it used to
  launder its own output), by the reserved list gaining `vpn`, `tunnel` and
  `wireguard` — the words an interface can legitimately be called — and by
  minting only names that fail `keepIface`, so the kernel's vocabulary never
  enters the replay at all.
- **The mint guard that makes the replay safe is cross-kind, and looks wrong.**
  A pass that runs after the replay can be handed a token, so `placeholder`
  returns a value unchanged when that value is a token it already minted — and
  it does so regardless of which kind is asking. Scoping it to the kind reads as
  a tightening and is a regression: a name that is both a profile and an
  endpoint is replayed as `profile-1` into `host=…`, where the endpoint pass
  offers it back under `host`, and a kind-scoped guard mints `host-2` for it —
  a token standing for a token, and a legend counting a hostname that is nowhere
  in the bundle. This was proposed during review and rejected on that evidence.
  Pinned by `TestAReplayedTokenIsNotReMintedUnderAnotherKind`.
- **The bundle's collection order stays load-bearing.** The replay can only
  replace what some earlier entry taught it, so `config.json` and `learned.json`
  must still be collected before `doctor.json` and `state.json`. Pinned by
  `TestTheBundleCollectsNameSourcesFirst`.
- **An interface known only to the live host** — autodetect, no daemon, so no
  `state.json` — is taught to the redactor by nothing, and the replay has nothing
  to replay. Stated in `internal/redact`'s package comment rather than implied,
  because a redactor must never claim coverage it does not have.
