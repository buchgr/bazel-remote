package azblobproxy

const (
	AuthMethodClientCertificate     = "client_certificate"
	AuthMethodClientSecret          = "client_secret"
	AuthMethodEnvironmentCredential = "environment_credential"
	AuthMethodDefault               = "default"
)

func GetAuthMethods() []string {
	return []string{
		AuthMethodClientCertificate,
		AuthMethodClientSecret,
		AuthMethodEnvironmentCredential,
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
