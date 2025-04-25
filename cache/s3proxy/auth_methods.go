package s3proxy

const (
	AuthMethodIAMRole            = "iam_role"
	AuthMethodAccessKey          = "access_key"
	AuthMethodAWSCredentialsFile = "aws_credentials_file"
	AuthMethodKubernetesIdentity = "kubernetes_identity"
)

func GetAuthMethods() []string {
	return []string{
		AuthMethodIAMRole,
		AuthMethodAccessKey,
		AuthMethodAWSCredentialsFile,
		AuthMethodKubernetesIdentity,
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
