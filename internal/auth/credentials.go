package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// dummyCredential is a minimal service account JSON key that satisfies
// GCP client library credential validation without reaching real GCP.
var dummyCredential = map[string]string{
	"type":                        "service_account",
	"project_id":                  "localgcp",
	"private_key_id":              "1",
	"private_key":                 "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF8PbnGy0AHB7MhgHcTz6sE2I2yPB\naFDrBz9vFqU4zK1cC2IfrhGVMOZ0BDkFaD1MYaKM8WFaoOaUyHjGFNrdL30bHkwZ\noYfkGOe4VVGGbNSlOSdTXx7IVJLJtQrJjMnJR5jYEVqXJMF/Ej3iJMmMsVMBJ4oJ\nvhLCZVqCn5P5Mj8NqVQ/e8B4mY3L3JBePshZLfSNz6L5ZJfpINpmrvNEbPjD4RdP\nU68XfagX7tnynblHBOKJTXl2G1H6yL8IA0V8Lqt0cNLzGLIa15JRofHWEMxJFRHp\nT3OlRkLFQEaV+B8+RLNWBiIhIAh+BwKE/6DstQIDAQABAoIBAC5RgZ+hBx7xHNaM\npPgwGMnFikH6BKUN32k2LfJMzSEhMCJJRbHjqkf7e3GCAB+XvMLAnJPsOWpmmhi4\npRDYBxzR2JhLMRYCPqgG2Pv+bJc5r0mVhJI/H7GF8V/ij5YN4XDGJMuNEYjttnf1\njZh1DMEKC2qq2fXHBowmNPjNiWJPZhGWGqbsDwTMKHHFVSagZJIhMVJlr84L5pH2\nq/3S9IDsad9PBe1jxZkJGe+mGWk12cgiZaGfGxL/IlPCm/M4g1UZ/5E5nZ1sBtME\njFBvBiF2W2OYhHl7bS2BXMxGj5aYt5AJEaFqcFsSHUslRKmh1hL/OV9bRkMOAN3z\nk/WeCoECgYEA7T3cHRXq3t1ZCjGQnOUjkGMFpCOwrKBhObrh3cXLqYn/60VDEoSm\njI0fCW9KRBNhMQ+4FE1ioP/p2MwQTAbDaJkt7R+GKe0+JXSxruPjsgk4NH7O2ECn\nmN/V9V/OBKk/bFBK81D3cV/B4fNvtkIzus99GXb+PI36d7CxXVA5GpUCgYEA4pif\nVb9TVzQQYqmAi3EHiDx8HJQpCv3v+m4MSONVN/yB1RNbVCQ7JlfYeUVyHGji1bJp\ntHkDU4BSXN/pB1s5NAj7WIXdpcBwTnaBK/OuOYRFzmNXx/GnFr2ATJGSK5TLC5i+\nMeB5N/bGnJJ4VIp0t8gn05/7hC7gKv+nNfSuXMECgYAJjGIKUwJM2K/yR7Rv2lOb\nasJ0m7y2HnYh5h0p5LRp8uf/I3I9ng0OREisBn2aHMFEQ9K/0XHwfOcK0200PY7Z\nnJhJPgSZ/UGUxBnCP47I0x0s2+aPbfKQD1a1p0pOHzbI7UzEHhLRQj5aVXvvE/6V1\ndZJqp47ziFGVIGbGwEHTSQKBgQCjbtTSAyKKxnU4efPFwN+VVPpCPhjgpj8F0Qod\nIVZWH4SpJB4FJSlhpS3MYoyNaVk1cNEjIGU2RLFEbXJqAMGT8WZl1QflCaC3mgki\npfkhLRABNYqIqJL0bxGl0SMRC5keSF4FzPE2MQkiR3yma3D0tF9l6zKw/gKVmjpN8\nMDUgAQKBgQDL75xGOQxUmhKQfsgWWxVQ7ot+u8d1Fp5V7jM7xJj/HGtq2BYZiEn\nhMW+7aS2EPpzQrxTk9VSRe2B+4uFvIlU2bPAI5+4BqE/vjnhPMz/bMlmVyy0s+z\nqMz0R8x3W4VYHjbGDjCAh9J6JJQSsaD7H3v2TEdV5YD7L2sKn5M+uw==\n-----END RSA PRIVATE KEY-----\n",
	"client_email":                "localgcp@localgcp.iam.gserviceaccount.com",
	"client_id":                   "1",
	"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
	"token_uri":                   "https://oauth2.googleapis.com/token",
	"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
	"client_x509_cert_url":        "https://www.googleapis.com/robot/v1/metadata/x509/localgcp%40localgcp.iam.gserviceaccount.com",
}

// EnsureCredentials writes the dummy credential file to the given directory.
// Returns the path to the written file.
func EnsureCredentials(dir string) (string, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		dir = filepath.Join(home, ".localgcp")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create directory %s: %w", dir, err)
	}

	credPath := filepath.Join(dir, "credentials.json")
	data, err := json.MarshalIndent(dummyCredential, "", "  ")
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(credPath, data, 0o600); err != nil {
		return "", fmt.Errorf("cannot write credentials to %s: %w", credPath, err)
	}

	return credPath, nil
}
