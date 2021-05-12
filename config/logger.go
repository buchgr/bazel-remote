package config

import (
	"io"
	"log"
	"os"
)

const (
	logFlags = log.Ldate | log.Ltime | log.LUTC
)

func (c *Config) setLogger() error {
	log.SetFlags(logFlags)

	c.AccessLogger = log.New(os.Stdout, "", logFlags)
	c.ErrorLogger = log.New(os.Stderr, "", logFlags)

	if c.DisableAccessLog {
		c.AccessLogger.SetOutput(io.Discard)
	}

	return nil
}
