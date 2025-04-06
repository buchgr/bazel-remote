package config

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"

	"github.com/buchgr/bazel-remote/v2/cache/azblobproxy"
	"github.com/buchgr/bazel-remote/v2/cache/gcsproxy"
	"github.com/buchgr/bazel-remote/v2/cache/grpcproxy"
	"github.com/buchgr/bazel-remote/v2/cache/httpproxy"
	"github.com/buchgr/bazel-remote/v2/cache/s3proxy"
	"github.com/minio/minio-go/v7"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	prom "github.com/prometheus/client_golang/prometheus"
)

func getTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	config := &tls.Config{}
	if certFile != "" && keyFile != "" {
		readCert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}

		config.Certificates = []tls.Certificate{readCert}
	}
	if caFile != "" {
		caCert, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		caCertPool := x509.NewCertPool()
		if added := caCertPool.AppendCertsFromPEM(caCert); !added {
			return nil, fmt.Errorf("Failed to add ca cert to cert pool.")
		}
		config.RootCAs = caCertPool
	}
	return config, nil
}

func (c *Config) setProxy() error {
	if c.GoogleCloudStorage != nil {
		proxyCache, err := gcsproxy.New(c.GoogleCloudStorage.Bucket,
			c.GoogleCloudStorage.UseDefaultCredentials, c.GoogleCloudStorage.JSONCredentialsFile,
			c.StorageMode, c.AccessLogger, c.ErrorLogger, c.NumUploaders, c.MaxQueuedUploads)
		if err != nil {
			return err
		}

		c.ProxyBackend = proxyCache
		return nil
	}

	if c.GRPCBackend != nil {
		var opts []grpc.DialOption
		if c.GRPCBackend.BaseURL.Scheme == "grpcs" {
			config, err := getTLSConfig(c.GRPCBackend.CertFile, c.GRPCBackend.KeyFile, c.GRPCBackend.CaFile)
			if err != nil {
				return err
			}
			creds := credentials.NewTLS(config)
			opts = append(opts, grpc.WithTransportCredentials(creds))
		} else {
			opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		}
		if password, ok := c.GRPCBackend.BaseURL.User.Password(); ok {
			username := c.GRPCBackend.BaseURL.User.Username()
			auth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", username, password)))
			header := fmt.Sprintf("Basic %s", auth)
			unaryAuth := func(ctx context.Context, method string, req, res interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				return invoker(metadata.AppendToOutgoingContext(ctx, "Authorization", header), method, req, res, cc, opts...)
			}
			streamAuth := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
				return streamer(metadata.AppendToOutgoingContext(ctx, "Authorization", header), desc, cc, method, opts...)
			}
			opts = append(opts, grpc.WithChainUnaryInterceptor(unaryAuth), grpc.WithStreamInterceptor(streamAuth))
		}

		metrics := grpc_prometheus.NewClientMetrics(func(o *prom.CounterOpts) { o.Namespace = "proxy" })
		metrics.EnableClientHandlingTimeHistogram(func(o *prom.HistogramOpts) { o.Namespace = "proxy" })
		err := prom.Register(metrics)
		if err != nil {
			return err
		}
		opts = append(opts, grpc.WithChainStreamInterceptor(metrics.StreamClientInterceptor()))
		opts = append(opts, grpc.WithChainUnaryInterceptor(metrics.UnaryClientInterceptor()))

		conn, err := grpc.NewClient(c.GRPCBackend.BaseURL.Host, opts...)
		if err != nil {
			return err
		}
		clients := grpcproxy.NewGrpcClients(conn)
		err = clients.CheckCapabilities(c.StorageMode == "zstd")
		if err != nil {
			return err
		}
		proxy := grpcproxy.New(clients, c.StorageMode,
			c.AccessLogger, c.ErrorLogger, c.NumUploaders, c.MaxQueuedUploads)

		c.ProxyBackend = proxy
	}

	if c.HTTPBackend != nil {
		httpClient := &http.Client{}
		if c.HTTPBackend.BaseURL.Scheme == "https" {
			config, err := getTLSConfig(c.HTTPBackend.CertFile, c.HTTPBackend.KeyFile, c.HTTPBackend.CaFile)
			if err != nil {
				return err
			}
			tr := &http.Transport{TLSClientConfig: config}
			httpClient.Transport = tr
		}

		proxyCache, err := httpproxy.New(c.HTTPBackend.BaseURL, c.StorageMode,
			httpClient, c.AccessLogger, c.ErrorLogger, c.NumUploaders, c.MaxQueuedUploads)
		if err != nil {
			return err
		}

		c.ProxyBackend = proxyCache
		return nil
	}

	if c.S3CloudStorage != nil {
		creds, err := c.S3CloudStorage.GetCredentials()
		if err != nil {
			return err
		}

		bucketLookupType, err := parseBucketLookupType(c.S3CloudStorage.BucketLookupType)
		if err != nil {
			return err
		}
		c.ProxyBackend = s3proxy.New(
			c.S3CloudStorage.Endpoint,
			c.S3CloudStorage.Bucket,
			bucketLookupType,
			c.S3CloudStorage.Prefix,
			creds,
			c.S3CloudStorage.DisableSSL,
			c.S3CloudStorage.UpdateTimestamps,
			c.S3CloudStorage.Region,
			c.S3CloudStorage.MaxIdleConns,
			c.StorageMode, c.AccessLogger, c.ErrorLogger, c.NumUploaders, c.MaxQueuedUploads)
		return nil
	}

	if c.AzBlobConfig != nil {
		creds, err := c.AzBlobConfig.GetCredentials()
		if err != nil {
			return err
		}

		c.ProxyBackend = azblobproxy.New(
			c.AzBlobConfig.StorageAccount,
			c.AzBlobConfig.ContainerName,
			c.AzBlobConfig.Prefix,
			creds,
			c.AzBlobConfig.SharedKey,
			c.AzBlobConfig.UpdateTimestamps,
			c.StorageMode, c.AccessLogger, c.ErrorLogger, c.NumUploaders, c.MaxQueuedUploads,
		)
		return nil
	}

	return nil
}

func parseBucketLookupType(typeStr string) (minio.BucketLookupType, error) {
	valMap := map[string]minio.BucketLookupType{
		"auto": minio.BucketLookupAuto,
		"dns":  minio.BucketLookupDNS,
		"path": minio.BucketLookupPath,
	}

	val, ok := valMap[typeStr]
	if !ok {
		return 0, fmt.Errorf("Unsupported bucket_lookup_type value : %s", typeStr)
	}

	return val, nil
}
