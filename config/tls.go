package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func (c *Config) setTLSConfig() error {

	// Only use TLS 1.2 or later on the server side. At the time of writing,
	// go defaults to using 1.0 as the min version when acting as a server.
	// TODO: consider raising this to 1.3, and possibly add a config flag?
	minTLSVersion := uint16(tls.VersionTLS12)

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

		c.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{readCert},
			ClientCAs:    caCertPool,

			// This allows us to handle some requests without a valid client
			// certificate (like the grpc health check service), but then we
			// need to explicitly check for verified certs on requests that
			// we require auth for.
			// See server.checkGRPCClientCert and httpCache.hasValidClientCert.
			ClientAuth: tls.VerifyClientCertIfGiven,

			MinVersion: minTLSVersion,
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
			MinVersion:   minTLSVersion,
		}

		return nil
	}

	return nil
}
