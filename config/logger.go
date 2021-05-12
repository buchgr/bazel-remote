package config

import (
	"io/ioutil"
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

	if c.AccessLogLevel == "none" {
		c.AccessLogger.SetOutput(ioutil.Discard)
	}

	return nil
}
