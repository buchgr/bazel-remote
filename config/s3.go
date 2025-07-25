package config

import (
	"fmt"
	"log"

	"github.com/buchgr/bazel-remote/v2/cache/s3proxy"

	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3CloudStorageConfig stores the configuration of an S3 API proxy backend.
type S3CloudStorageConfig struct {
	Endpoint                 string `yaml:"endpoint"`
	Bucket                   string `yaml:"bucket"`
	Prefix                   string `yaml:"prefix"`
	AuthMethod               string `yaml:"auth_method"`
	AccessKeyID              string `yaml:"access_key_id"`
	SecretAccessKey          string `yaml:"secret_access_key"`
	SessionToken             string `yaml:"session_token"`
	SignatureType            string `yaml:"signature_type"`
	DisableSSL               bool   `yaml:"disable_ssl"`
	UpdateTimestamps         bool   `yaml:"update_timestamps"`
	IAMRoleEndpoint          string `yaml:"iam_role_endpoint"`
	Region                   string `yaml:"region"`
	KeyVersion               *int   `yaml:"key_version"`
	AWSProfile               string `yaml:"aws_profile"`
	AWSSharedCredentialsFile string `yaml:"aws_shared_credentials_file"`
	BucketLookupType         string `yaml:"bucket_lookup_type"`
	MaxIdleConns             int    `yaml:"max_idle_conns"`
}

func (s3c S3CloudStorageConfig) GetCredentials() (*credentials.Credentials, error) {
	if s3c.AuthMethod == s3proxy.AuthMethodAWSCredentialsFile {
		log.Println("S3 Credentials: using AWS credentials file.")
		return credentials.NewFileAWSCredentials(s3c.AWSSharedCredentialsFile, s3c.AWSProfile), nil
	} else if s3c.AuthMethod == s3proxy.AuthMethodAccessKey {
		if s3c.AccessKeyID == "" {
			return nil, fmt.Errorf("missing s3.access_key_id for s3.auth_method = '%s'", s3proxy.AuthMethodAccessKey)
		}
		if s3c.SecretAccessKey == "" {
			return nil, fmt.Errorf("missing s3.secret_access_key for s3.auth_method = '%s'", s3proxy.AuthMethodAccessKey)
		}
		log.Println("S3 Credentials: using access/secret access key.")
		signatureType := parseSignatureType(s3c.SignatureType)
		log.Printf("S3 Sign: using %s sign\n", signatureType.String())
		return credentials.NewStatic(s3c.AccessKeyID, s3c.SecretAccessKey, s3c.SessionToken, signatureType), nil
	} else if s3c.AuthMethod == s3proxy.AuthMethodIAMRole {
		// Fall back to getting credentials from IAM
		log.Println("S3 Credentials: using IAM.")
		return credentials.NewIAM(s3c.IAMRoleEndpoint), nil
	}

	return nil, fmt.Errorf("invalid s3.auth_method: %s", s3c.AuthMethod)
}

func parseSignatureType(str string) credentials.SignatureType {
	// str has been verified in config.go/validateConfig, must be one of these keys
	valMap := map[string]credentials.SignatureType{
		"":            credentials.SignatureV4,
		"v2":          credentials.SignatureV2,
		"v4":          credentials.SignatureV4,
		"v4streaming": credentials.SignatureV4Streaming,
		"anonymous":   credentials.SignatureAnonymous,
	}
	return valMap[str]
}
