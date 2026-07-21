//go:build darwin

package daemon

// maxSocketPathLen is the longest unix-domain socket path bind(2) accepts on
// darwin. sockaddr_un.sun_path is 104 bytes there, one of which is the NUL
// terminator, leaving 103 usable. A longer path fails net.Listen with a bare
// "bind: invalid argument" that reads like a permissions or path bug (#68).
const maxSocketPathLen = 103
