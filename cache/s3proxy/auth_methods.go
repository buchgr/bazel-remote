package s3proxy

const (
	AuthMethodIAMRole            = "iam_role"
	AuthMethodAccessKey          = "access_key"
	AuthMethodAWSCredentialsFile = "aws_credentials_file"
)

func GetAuthMethods() []string {
	return []string{
		AuthMethodIAMRole,
		AuthMethodAccessKey,
		AuthMethodAWSCredentialsFile,
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
