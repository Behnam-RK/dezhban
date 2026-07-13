package control

// secureSocket is a no-op on Windows: there is no chown/mode model to apply, and
// the socket lives under the ProgramData directory whose ACL already restricts
// writers to administrators (the same assumption internal/command makes for the
// command file). The passwordless control path is not a supported Windows feature
// today — it is wired only because the daemon shares one run loop across OSes.
func secureSocket(path, group string) error { return nil }
