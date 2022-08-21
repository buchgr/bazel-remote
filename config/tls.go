package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func (c *Config) setTLSConfig() error {
	if len(c.TLSCaFile) != 0 {
		caCertPool := x509.NewCertPool()
		caCert, err := os.ReadFile(c.TLSCaFile)
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

		var cat tls.ClientAuthType = tls.RequireAndVerifyClientCert
		if c.AllowUnauthenticatedReads {
			// This allows us to handle some requests without a valid client
			// certificate, but then we need to explicitly check for verified
			// certs on requests that we require auth for.
			// See server.checkGRPCClientCert and httpCache.hasValidClientCert.
			cat = tls.VerifyClientCertIfGiven
		}

		c.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{readCert},
			ClientCAs:    caCertPool,
			ClientAuth:   cat,
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
