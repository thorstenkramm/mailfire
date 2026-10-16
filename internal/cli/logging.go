package cli

import (
	"io"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/logging"
)

func openLogger(cfg *config.Config) (*logging.Logger, error) {
	if cfg.Logfile == "" {
		return nil, nil
	}
	return logging.Open(cfg.Logfile, cfg.LogLevel, cfg.LogMaxSize)
}

func closeLogger(logger *logging.Logger, stderr io.Writer) {
	if logger != nil {
		closeReported(logger, stderr)
	}
}
