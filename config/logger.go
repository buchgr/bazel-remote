package config

import (
	"io"
	"log"
	"os"
)

func (c *Config) setLogger() error {

	var logFlags int
	switch c.LogTimezone {
	case "UTC":
		logFlags = log.Ldate | log.Ltime | log.LUTC
	case "local":
		logFlags = log.Ldate | log.Ltime
	case "none":
		logFlags = 0
	}

	log.SetFlags(logFlags)

	c.AccessLogger = log.New(os.Stdout, "", logFlags)
	c.ErrorLogger = log.New(os.Stderr, "", logFlags)

	if c.AccessLogLevel == "none" {
		c.AccessLogger.SetOutput(io.Discard)
	}

	return nil
}
