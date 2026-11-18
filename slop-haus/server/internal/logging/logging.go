// Package logging is the api's wiring of go-privacy/otellog: text to stderr
// always, and -- when an OTLP endpoint is configured -- the same records
// exported to the collector that routes them by tier to VictoriaLogs.
package logging

import (
	"io"
	"log/slog"

	"github.com/lowaa/slop-factory/go-privacy/otellog"
)

// ServiceName is the service.name every exported record carries, the field
// VictoriaLogs groups a stream by.
const ServiceName = "slop-haus-api"

// New builds the api's logs. otlpEndpoint is the full URL records are POSTed
// to, path included (http://otel-collector:4318/v1/logs); empty exports
// nothing. See otellog.New.
func New(otlpEndpoint string, stderr io.Writer) (*otellog.Logs, error) {
	return otellog.New(&otellog.Options{
		ServiceName:  ServiceName,
		OTLPEndpoint: otlpEndpoint,
		Output:       stderr,
		Format:       otellog.FormatText,
	})
}

// DevMailHandler is where the local-development mail sink (EMAIL_BACKEND=log)
// writes whole messages to w. See otellog.DevMailHandler.
func DevMailHandler(w io.Writer) slog.Handler { return otellog.DevMailHandler(w, otellog.FormatText) }
