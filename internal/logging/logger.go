package logging

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

var globalLogger zerolog.Logger

func init() {
	// Configure global logger with pretty console output in development
	output := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.RFC3339,
		NoColor:    false,
	}

	// Use JSON in production (set MIMORI_LOG_FORMAT=json)
	if os.Getenv("MIMORI_LOG_FORMAT") == "json" {
		globalLogger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	} else {
		globalLogger = zerolog.New(output).With().Timestamp().Logger()
	}

	// Set log level from env (default: info)
	level := os.Getenv("MIMORI_LOG_LEVEL")
	switch level {
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

// Logger returns a logger with context fields
func Logger() *zerolog.Logger {
	return &globalLogger
}

// With returns a logger context for chaining fields
func With() zerolog.Context {
	return globalLogger.With()
}

// Debug logs a debug message
func Debug(msg string) {
	globalLogger.Debug().Msg(msg)
}

// Info logs an info message
func Info(msg string) {
	globalLogger.Info().Msg(msg)
}

// Warn logs a warning message
func Warn(msg string) {
	globalLogger.Warn().Msg(msg)
}

// Error logs an error message
func Error(msg string) {
	globalLogger.Error().Msg(msg)
}

// Fatal logs a fatal message and exits
func Fatal(msg string) {
	globalLogger.Fatal().Msg(msg)
}

// WithRaftContext returns a logger with Raft-specific context fields
func WithRaftContext(nodeID string, term int, state string) *zerolog.Logger {
	logger := globalLogger.With().
		Str("node_id", nodeID).
		Int("term", term).
		Str("state", state).
		Logger()
	return &logger
}

// WithError returns a logger context with an error field
func WithError(err error) *zerolog.Event {
	return globalLogger.Error().Err(err)
}
