package config

import (
	"net"
)

// ListenSidecar binds only the OS-selected loopback port used by the Electron
// supervisor. A fixed or externally supplied listener is not a valid daemon
// transport because it can be pre-bound or discovered by another process.
func (value HTTP) ListenSidecar() (net.Listener, error) {
	if value.ListenHost != DefaultListenHost || value.ListenPort != 0 {
		return nil, newError("sidecar_listener_configuration_invalid")
	}
	listener, err := net.Listen("tcp4", value.Address())
	if err != nil {
		return nil, err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address == nil || address.IP == nil || !address.IP.Equal(net.ParseIP(DefaultListenHost)) || address.Port <= 0 {
		_ = listener.Close()
		return nil, newError("sidecar_listener_invalid")
	}
	return listener, nil
}
