//go:build !darwin

package config

// defaultControlGroup is empty off macOS: there is no single portable "the admins
// of this machine" group (wheel, sudo, adm all mean different things across
// distros), and guessing wrong would either break the socket or hand it to the
// wrong people. An empty group means root-only (0600) — the passwordless path
// stays off until an operator names a group explicitly in control.group.
const defaultControlGroup = ""
