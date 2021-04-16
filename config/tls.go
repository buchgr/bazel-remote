package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
)

func (c *Config) GetTLSConfig() (*tls.Config, error) {
	if len(c.TLSCaFile) != 0 {
		caCertPool := x509.NewCertPool()
		caCert, err := ioutil.ReadFile(c.TLSCaFile)
		if err != nil {
			return nil, fmt.Errorf("Error reading TLS CA File: %w", err)
		}
		added := caCertPool.AppendCertsFromPEM(caCert)
		if !added {
			return nil, fmt.Errorf("Failed to add certificate to cert pool.")
		}

		readCert, err := tls.LoadX509KeyPair(
			c.TLSCertFile,
			c.TLSKeyFile,
		)
		if err != nil {
			return nil, fmt.Errorf("Error reading certificate/key pair: %w", err)
		}

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{readCert},
			ClientCAs:    caCertPool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
		}

		return tlsConfig, nil
	}

	if len(c.TLSCertFile) != 0 && len(c.TLSKeyFile) != 0 {
		readCert, err := tls.LoadX509KeyPair(
			c.TLSCertFile,
			c.TLSKeyFile,
		)
		if err != nil {
			return nil, fmt.Errorf("Error reading certificate/key pair: %w", err)
		}

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{readCert},
		}

		return tlsConfig, nil
	}

	return nil, nil
}
