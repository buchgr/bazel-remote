package azblobproxy

const (
	AuthMethodClientCertificate     = "client_certificate"
	AuthMethodClientSecret          = "client_secret"
	AuthMethodEnvironmentCredential = "environment_credential"
	AuthMethodDeviceCode            = "device_code"
	AuthMethodDefault               = "default"
)

func GetAuthMethods() []string {
	return []string{
		AuthMethodClientCertificate,
		AuthMethodClientSecret,
		AuthMethodEnvironmentCredential,
		AuthMethodDeviceCode,
		AuthMethodDefault,
	}
}

func IsValidAuthMethod(authMethod string) bool {
	for _, b := range GetAuthMethods() {
		if authMethod == b {
			return true
		}
	}
	return false
}
