package persistence

import (
	"errors"
	"fmt"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

// Config controls persistence store setup.
type Config struct {
	Database config.DatabaseConfig
}

// Validate checks persistence config basics.
func (c Config) Validate() error {
	if !c.Database.Enabled {
		return nil
	}
	if c.Database.URL == "" {
		return errors.New("database url required when enabled")
	}
	if c.Database.MaxConns < 0 {
		return fmt.Errorf("database max_conns must be >= 0")
	}
	return nil
}
