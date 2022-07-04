package config

import (
	"fmt"
	"log"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/buchgr/bazel-remote/cache/azblobproxy"
)

type AzBlobStorageConfig struct {
	StorageAccount string `yaml:"storage_account"`
	ContainerName  string `yaml:"container_name"`
	Prefix         string `yaml:"prefix"`
	AuthMethod     string `yaml:"auth_method"`
	TenantID       string `yaml:"tenant_id"`
	ClientID       string `yaml:"client_id"`
	ClientSecret   string `yaml:"client_secret"`
	CertPath       string `yaml:"cert_path"`
}

func (azblobc AzBlobStorageConfig) GetCredentials() (azcore.TokenCredential, error) {
	if azblobc.AuthMethod == azblobproxy.AuthMethodDefault {
		log.Println("AzBlob Credentials: using Default Credentials")
		return azidentity.NewDefaultAzureCredential(nil)
	}

	if azblobc.AuthMethod == azblobproxy.AuthMethodClientCertificate {
		log.Println("AzBlob Credentials: using client certificate credentials")

		certData, err := os.ReadFile(azblobc.CertPath)
		if err != nil {
			return nil, fmt.Errorf(`failed to read certificate file "%s": %v`, azblobc.CertPath, err)
		}
		certs, key, err := azidentity.ParseCertificates(certData, nil)
		if err != nil {
			return nil, fmt.Errorf(`failed to load certificate from "%s": %v`, azblobc.CertPath, err)
		}
		return azidentity.NewClientCertificateCredential(azblobc.TenantID, azblobc.ClientID, certs, key, nil)
	}

	if azblobc.AuthMethod == azblobproxy.AuthMethodClientSecret {
		log.Println("AzBlob Credentials: using client secret credentials")
		return azidentity.NewClientSecretCredential(azblobc.TenantID, azblobc.ClientID, azblobc.ClientSecret, nil)
	}

	if azblobc.AuthMethod == azblobproxy.AuthMethodEnvironmentCredential {
		log.Println("AzBlob Credentials: using client secret credentials")
		return azidentity.NewEnvironmentCredential(nil)
	}

	return nil, fmt.Errorf("invalid azblob.auth_method: %s", azblobc.AuthMethod)
}
