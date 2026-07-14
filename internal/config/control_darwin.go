package config

// defaultControlGroup is the unix group allowed to drive the daemon over the
// control socket. On macOS that is "admin": the group every administrator account
// is in, and the same set of humans macOS would have prompted for a password
// anyway. Making it the default is what removes the prompt from routine ops.
const defaultControlGroup = "admin"
