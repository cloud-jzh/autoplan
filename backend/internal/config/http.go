package config

import (
	"io"
	"log"
	"net/http"
)

const MaximumHeaderBytes = 64 << 10

// NewServer transfers the validated central HTTP configuration into a server.
// It never substitutes a wildcard address or zero timeout on invalid input.
func (value HTTP) NewServer(handler http.Handler) (*http.Server, error) {
	if handler == nil || value.ListenHost != DefaultListenHost || value.ListenPort < 0 || value.ListenPort > 65535 ||
		value.ReadHeaderTimeout <= 0 || value.ReadTimeout <= 0 || value.WriteTimeout <= 0 ||
		value.IdleTimeout <= 0 || value.ShutdownTimeout <= 0 || value.BodyLimitBytes <= 0 ||
		value.BodyLimitBytes > MaximumBodyLimit || value.ReadHeaderTimeout > MaximumHTTPTimeout ||
		value.ReadTimeout > MaximumHTTPTimeout || value.WriteTimeout > MaximumHTTPTimeout ||
		value.IdleTimeout > MaximumHTTPTimeout || value.ShutdownTimeout > MaximumCloseTimeout {
		return nil, newError("http_server_configuration_invalid")
	}
	return &http.Server{
		Addr:              value.Address(),
		Handler:           handler,
		ReadHeaderTimeout: value.ReadHeaderTimeout,
		ReadTimeout:       value.ReadTimeout,
		WriteTimeout:      value.WriteTimeout,
		IdleTimeout:       value.IdleTimeout,
		MaxHeaderBytes:    MaximumHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
	}, nil
}
