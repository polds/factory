package github

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LoadPrivateKey reads an unencrypted RSA private key (PKCS#1 or PKCS#8 PEM)
// from path. The file is the value of FACTORY_GITHUB_APP_PRIVATE_KEY_FILE.
func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("github: read private key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("github: private key pem is missing")
	}
	if strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED") {
		return nil, errors.New("github: private key must be unencrypted")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("github: parse private key: %w", err)
		}
		return key, nil
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("github: parse private key: %w", err)
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("github: private key is %T, want RSA", parsed)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("github: unsupported private key type %q", block.Type)
	}
}

// ConfigFromEnv reads FACTORY_GITHUB_APP_ID and FACTORY_GITHUB_APP_PRIVATE_KEY_FILE.
func ConfigFromEnv() (Config, error) {
	rawID := strings.TrimSpace(os.Getenv("FACTORY_GITHUB_APP_ID"))
	if rawID == "" {
		return Config{}, errors.New("github: FACTORY_GITHUB_APP_ID is required")
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		return Config{}, errors.New("github: FACTORY_GITHUB_APP_ID must be a positive integer")
	}
	path := strings.TrimSpace(os.Getenv("FACTORY_GITHUB_APP_PRIVATE_KEY_FILE"))
	if path == "" {
		return Config{}, errors.New("github: FACTORY_GITHUB_APP_PRIVATE_KEY_FILE is required")
	}
	key, err := LoadPrivateKey(path)
	if err != nil {
		return Config{}, err
	}
	return Config{AppID: id, Key: key}, nil
}
