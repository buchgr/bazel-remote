package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
)

func (c *Config) setTLSConfig() error {
	if len(c.TLSCaFile) != 0 {
		caCertPool := x509.NewCertPool()
		caCert, err := ioutil.ReadFile(c.TLSCaFile)
		if err != nil {
			return fmt.Errorf("Error reading TLS CA File: %w", err)
		}
		added := caCertPool.AppendCertsFromPEM(caCert)
		if !added {
			return fmt.Errorf("Failed to add certificate to cert pool.")
		}

		readCert, err := tls.LoadX509KeyPair(
			c.TLSCertFile,
			c.TLSKeyFile,
		)
		if err != nil {
			return fmt.Errorf("Error reading certificate/key pair: %w", err)
		}

		c.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{readCert},
			ClientCAs:    caCertPool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
		}

		return nil
	}

	if len(c.TLSCertFile) != 0 && len(c.TLSKeyFile) != 0 {
		readCert, err := tls.LoadX509KeyPair(
			c.TLSCertFile,
			c.TLSKeyFile,
		)
		if err != nil {
			return fmt.Errorf("Error reading certificate/key pair: %w", err)
		}

		c.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{readCert},
		}

		return nil
	}

	return nil
}
