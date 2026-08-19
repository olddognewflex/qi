//go:build !darwin

package daemon

// maxSocketPathLen is the longest unix-domain socket path bind(2) accepts off
// darwin. sockaddr_un.sun_path is 108 bytes on Linux (and Windows AF_UNIX),
// leaving 107 usable after the NUL terminator. See socket_darwin.go and #68.
const maxSocketPathLen = 107
