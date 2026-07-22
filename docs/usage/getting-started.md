<p align="center">
  <img src="../../gui/assets/png/banner-1280x640.png" alt="Dezhban — system-wide network kill switch" width="640">
</p>

# Quick start

Get dezhban protecting your machine in about ten minutes — without locking
yourself out on the way there.

**What it does:** dezhban makes sure your traffic can only leave this machine
through your VPN tunnel. If the VPN drops, the connection is cut instantly
rather than silently falling back to your real IP. If the VPN reconnects
somewhere you've told it to refuse, everything stops.

> [!TIP]
> Every step below marked **safe** touches nothing — no firewall rules, no root.
> You can run all of them on a working machine to see exactly what *would*
> happen before anything is armed.

---

## 1. Install it

**macOS** — download the `.pkg` from the
[Releases page](https://github.com/Behnam-RK/dezhban/releases) and install it.
You'll be asked for your password once.

**Linux / Windows** — download the binary for your platform from the same page.

Installing does **not** start enforcement. A kill switch configured by guesswork
is how you get locked out of your own machine, so arming is always a separate,
deliberate step. Full options, including the Gatekeeper workaround for the
unsigned macOS build: [Install](install.md).

Check it's there:

```sh
dezhban version
```

---

## 2. Set it up

The wizard writes the config for you — no JSON by hand:

```sh
sudo dezhban setup
```

It asks which countries to refuse, and how to find your VPN. If you already have
a VPN config file, import it instead of typing endpoints:

```sh
dezhban vpn import ~/wg0.conf     # WireGuard .conf, OpenVPN .ovpn, or V2Ray JSON
```

Prefer to write the file yourself? Start from `configs/dezhban.example.json` and
see the [config reference](config.md).

---

## 3. Look before you leap

This is the part worth not skipping. All four commands are **safe** — read-only,
no root, no firewall changes.

```sh
dezhban validate       # is my config actually valid?
dezhban doctor         # would this config lock me out?
dezhban print-rules --mode guard    # show the exact rules, don't apply them
dezhban monitor        # live view: my IP, country, tunnel, verdict
```

`doctor` is the one that saves you. It looks for the specific mistakes that
produce a machine you can't reach — a VPN endpoint that only exists *inside* the
tunnel, a guard with no endpoints to hand, a tunnel interface that isn't there.
Add `--discover` to have it suggest fixes from what it can see on the host:

```sh
dezhban doctor --discover
```

`monitor` is the honest preview: it polls exactly like the real daemon and tells
you what verdict it *would* reach, while touching nothing.

---

## 4. Arm it

```sh
sudo dezhban start     # run as a background service, survives reboot
```

Or run it in the foreground to watch it work:

```sh
sudo dezhban run
```

Check on it any time:

```sh
dezhban status
```

---

## 5. Read the icon

On macOS the menubar tells you the posture at a glance. There are four looks:

| | Posture | What it means |
|---|---|---|
| <img src="../../gui/assets/png/menubar-on-color-88px.png" alt="Guard armed" height="22"> | **GUARD** | The healthy state. The tunnel is up and it is the only way out of this machine. |
| <img src="../../gui/assets/png/menubar-off-color-88px.png" alt="Standby" height="22"> | **STANDBY** | **Not protecting.** No rules are installed and your network is fully open. This is the resting state before a VPN has ever connected — it arms itself the moment one does. |
| <img src="../../gui/assets/png/menubar-blocked-color-88px.png" alt="Egress blocked" height="22"> | **FULL BLOCK** | Traffic is cut. Either the VPN's exit landed in a country you refused, or the guard is holding a dropped tunnel closed. Your VPN can still reconnect. |
| <img src="../../gui/assets/png/menubar-warning-color-88px.png" alt="Warning" height="22"> | **SWITCH WINDOW** | A bounded relaxation is open and **your real IP may be exposed** until it closes on its own. Also shown if a firewall action failed. |

Two things worth knowing, because they surprise people:

- **Grey does not mean "off by mistake."** Grey is the truthful "nothing is being
  enforced" look, and dezhban shows it rather than a reassuring icon whenever
  that's the case — including standby.
- **A dropped VPN turns the icon red, not green,** even though the posture is
  still `guard`. The guard is doing its job — physical egress is cut until the
  VPN returns — and that should be impossible to miss.

---

## 6. Everyday things

### Connecting a different VPN

A brand-new VPN server can't complete its handshake through a closed guard, so
open a short window for it:

```sh
sudo dezhban switch                  # opens a window — connect it in its own app now
dezhban switch --status              # is one open?
sudo dezhban switch --cancel         # close it early
```

The window closes itself as soon as a good exit is confirmed, and expires on its
own regardless. dezhban learns the new server so you won't need a window next
time.

### When the VPN drops on its own

Nothing to do. The guard cuts egress the moment the tunnel goes, then opens a
bounded reconnect window (30s by default) so your VPN client can redial. If it
comes back, the window closes early. If it doesn't, the guard stays shut.

Want zero relaxation ever, at the cost of reconnecting by hand? Set both windows
off:

```json
{ "vpn": { "switchWindow": "0", "reconnectWindow": "0" } }
```

### Reaching your printer, NAS, and router

Local network access stays on by default, so LAN devices keep working while the
guard is armed — this traffic never leaves the building. On an untrusted network
(a café, a hotel) you may not want that, since it also lets those devices reach
you:

```json
{ "vpn": { "allowLocalNetwork": false } }
```

---

## If you get locked out

This always works. It needs no daemon, no socket, and no working network:

```sh
sudo dezhban panic
```

It removes every rule dezhban installed and nothing else. Keep it in mind before
you need it — the full runbook is in
[troubleshooting.md](troubleshooting.md).

---

## Where to go next

| If you want to… | Read |
|---|---|
| Understand how the machine actually works | [how-it-works.md](../concepts/how-it-works.md) |
| See every posture and its exact ruleset | [modes.md](../concepts/modes.md) |
| Look up a config field | [config.md](config.md) |
| Find a command or flag | [cli.md](cli.md) |
| Recover from a lockout | [troubleshooting.md](troubleshooting.md) |
| Know why it's built this way | [architecture.md](../contribute/architecture.md) · [adr/](../adr/README.md) |
