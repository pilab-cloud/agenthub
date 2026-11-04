// Package logging configures the zerolog logger for AgentHub.
package logging

import (
	"io"
	"os"
	"strings"

	"github.com/rs/zerolog"
)

// New builds a zerolog logger from the configured level and format ("json" or
// "console"). It also installs it as the global logger.
func New(level, format string) (zerolog.Logger, error) {
	lvl, err := zerolog.ParseLevel(strings.ToLower(level))
	if err != nil {
		return zerolog.Logger{}, err
	}
	var w io.Writer = os.Stderr
	if strings.ToLower(format) == "console" {
		w = zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05.000"}
	}
	logger := zerolog.New(w).Level(lvl).With().Timestamp().Logger()
	zerolog.SetGlobalLevel(lvl)
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
	return logger, nil
}
