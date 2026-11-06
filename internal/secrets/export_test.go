package secrets

// HandleForTest constructs a Handle for redaction tests. Test-only.
func HandleForTest(name, value string) Handle {
	return newHandle(name, value)
}
