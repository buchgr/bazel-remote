package config

import (
	"io"
	"log"
	"os"
)

const (
	LogFlags = log.Ldate | log.Ltime | log.LUTC
)

func (c *Config) setLogger() error {
	c.AccessLogger = log.New(os.Stdout, "", LogFlags)
	c.ErrorLogger = log.New(os.Stderr, "", LogFlags)

	if c.AccessLogLevel == "none" {
		c.AccessLogger.SetOutput(io.Discard)
	}

	return nil
}
