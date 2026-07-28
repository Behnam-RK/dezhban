# Using the CLI without sudo

Most day-to-day commands — `block`, `unblock`, `switch`, `pause`, `resume`,
`hold` — don't actually need root once dezhban is running. They ask the
running daemon over its **control socket** instead, and the daemon performs
them. Whether that path is open for you depends on one setting:
`control.group`.

## Is it already on?

It depends on your OS. `control.group` defaults to `"admin"` on macOS, where
that group exists on every install and is exactly the set of accounts that can
already `sudo` — so **on macOS this is on out of the box** and there is
nothing to do. Off macOS it defaults to empty, because there is no single
portable name for "the admins" (Debian calls it `sudo`, Fedora calls it
`wheel`), and empty means the socket is root-only (mode `0600`). Every routine
command then falls back to `sudo`, which is why a fresh Linux install still
asks for a password for things that feel like they shouldn't need it. Turning
it on is one line.

Either way, `dezhban doctor`'s `control:` line is the authority on which state
you're actually in — skip to [Turn it on](#turn-it-on)'s verification steps if
you just want to check.

## Turn it on

First, identify your system's existing administrators group — the same one
that already lets you run `sudo` at all:

| System | Group | Default |
|---|---|---|
| Debian, Ubuntu | `sudo` | not set |
| Fedora, RHEL, Arch, openSUSE | `wheel` | not set |
| macOS | `admin` | **already set** |

Point `control.group` at it:

```sh
sudo dezhban config set control.group sudo   # or wheel / admin
```

Confirm you're actually a member (you almost certainly already are, since
this is the same group sudo itself uses):

```sh
id -nG
```

Confirm dezhban agrees:

```sh
dezhban doctor
```

Look for the `control:` line. `reachable (…, group "sudo") — routine ops
need no password` means it worked. Anything else names exactly what's
still missing — see [troubleshooting](#troubleshooting) below.

That's it. `dezhban block`, `unblock`, `switch`, `pause`, `resume`, and `hold`
now run with no password prompt, from your own account.

## What this actually grants — and what it doesn't

**No new authority is created here.** Membership in your distro's admin group
already lets you run any command as root via `sudo` — including
`sudo dezhban unblock` today. Pointing `control.group` at that same group
doesn't hand out a new capability; it removes a password prompt for
capabilities those accounts already had.

This is a different model from a tool that creates its **own** dedicated
group for this purpose — Docker's postinstall does exactly that, and Docker's
own documentation is direct about the cost: membership in the `docker` group
is "equivalent to root" and "grants root-level privileges." dezhban never
creates a group, never runs `usermod`, and never asks you to add an
unprivileged account to anything. If you can already `sudo`, you're already a
member of whatever group you point this at — that's the whole point.

Be clear-eyed about what membership *does* let someone do without a password:
**relax the kill switch** — open a switch/redial window, pause enforcement,
or lift a block. That's real, and it's why this setting must only ever point
at a group whose members could already do the equivalent thing through
`sudo` — never a group created just for this, and never a group that
includes accounts you wouldn't otherwise trust with root.

## What still needs root, and why

A handful of commands intentionally stay outside the control socket
entirely, no matter how `control.group` is set:

- `panic` — the lockout escape hatch. It removes every firewall rule with no
  daemon involved at all, deliberately independent of the very socket it
  would otherwise need to work.
- `install`, `uninstall`, `start`, `stop`, `restart` — a daemon can't manage
  its own service lifecycle.
- `vpn add`, `vpn remove`, `vpn promote`, `vpn forget`, `vpn import` — these
  write to files the daemon owns.
- `token enroll`, `token forget` — the control token that lets a *macOS Touch
  ID* prompt substitute for `sudo` on config writes; enrolling one is itself
  a privileged, once-per-setup action.
- `upgrade download`, `upgrade apply` — the staging directory is root-owned
  on purpose, so a local account can't swap the verified package before it's
  installed.

All of these auto-elevate under `sudo` on their own when you're at a real
terminal (see [cli.md § Do I need a password?](cli.md#do-i-need-a-password)) —
this page is only about the routine ops that don't have to.

## Troubleshooting

- **`disabled (control.enabled=false)`** — the socket itself is off:
  `dezhban config set control.enabled true`.
- **`unreachable`** — no daemon is listening yet. Start one
  (`sudo dezhban start`), or check that `control.socket` (if you've set a
  custom path) actually exists.
- **`reachable, but you are not in the "<group>" group`** — `id -nG` doesn't
  list it. Add your account to that group the normal way for your OS (the
  same step that already lets you `sudo`), then log out and back in — group
  membership is read at login, not live.
- **`reachable, but no group is configured`** — `control.group` is still
  empty; follow [Turn it on](#turn-it-on) above.
- **A specific op still asks for a password** — check `control.allowSwitchOps`
  and `control.allowPauseOps`. Either can independently force `switch`/`pause`
  ops back to `sudo` without touching the socket itself; `dezhban doctor`'s
  `control:` section names which ones are set that way.

See [config.md](config.md#control-block) for the full `control.*` reference.
