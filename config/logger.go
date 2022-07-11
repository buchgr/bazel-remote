package config

import (
	"io/ioutil"
	"log"
	"os"
)

const (
	LogFlags = log.Ldate | log.Ltime
)

func (c *Config) setLogger() error {
	c.AccessLogger = log.New(os.Stdout, "", LogFlags)
	c.ErrorLogger = log.New(os.Stderr, "", LogFlags)

	if c.AccessLogLevel == "none" {
		c.AccessLogger.SetOutput(ioutil.Discard)
	}

	return nil
}
