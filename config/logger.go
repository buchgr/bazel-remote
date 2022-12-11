package config

import (
	"io"
	"log"
	"os"
)

func (c *Config) setLogger() error {
	logFlags := log.Ldate | log.Ltime | log.LUTC
	if c.LogTimezone == "local" {
		logFlags = log.Ldate | log.Ltime
	}
	log.SetFlags(logFlags)

	c.AccessLogger = log.New(os.Stdout, "", logFlags)
	c.ErrorLogger = log.New(os.Stderr, "", logFlags)

	if c.AccessLogLevel == "none" {
		c.AccessLogger.SetOutput(io.Discard)
	}

	return nil
}
