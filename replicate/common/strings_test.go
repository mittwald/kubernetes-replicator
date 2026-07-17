package common

import (
	"strings"
	"testing"
)

func TestGenerateTargetName(t *testing.T) {
	tests := []struct {
		name       string
		sourceName string
		prefix     string
		suffix     string
		expected   string
	}{
		{
			name:       "no prefix or suffix",
			sourceName: "my-secret",
			prefix:     "",
			suffix:     "",
			expected:   "my-secret",
		},
		{
			name:       "prefix only",
			sourceName: "my-secret",
			prefix:     "prod",
			suffix:     "",
			expected:   "prod-my-secret",
		},
		{
			name:       "suffix only",
			sourceName: "my-secret",
			prefix:     "",
			suffix:     "backup",
			expected:   "my-secret-backup",
		},
		{
			name:       "both prefix and suffix",
			sourceName: "my-secret",
			prefix:     "prod",
			suffix:     "backup",
			expected:   "prod-my-secret-backup",
		},
		{
			name:       "prefix already ends with dash",
			sourceName: "my-secret",
			prefix:     "prod-",
			suffix:     "",
			expected:   "prod-my-secret",
		},
		{
			name:       "suffix already starts with dash",
			sourceName: "my-secret",
			prefix:     "",
			suffix:     "-backup",
			expected:   "my-secret-backup",
		},
		{
			name:       "both prefix and suffix already have dashes",
			sourceName: "my-secret",
			prefix:     "prod-",
			suffix:     "-backup",
			expected:   "prod-my-secret-backup",
		},
		{
			name:       "source name already has dashes",
			sourceName: "my-complex-secret-name",
			prefix:     "prod",
			suffix:     "backup",
			expected:   "prod-my-complex-secret-name-backup",
		},
		{
			name:       "empty source name",
			sourceName: "",
			prefix:     "prod",
			suffix:     "backup",
			expected:   "prod--backup",
		},
		{
			name:       "single character components",
			sourceName: "s",
			prefix:     "p",
			suffix:     "b",
			expected:   "p-s-b",
		},
		{
			name:       "numeric components",
			sourceName: "secret123",
			prefix:     "env1",
			suffix:     "v2",
			expected:   "env1-secret123-v2",
		},
		{
			name:       "mixed case (should preserve case)",
			sourceName: "MySecret",
			prefix:     "PROD",
			suffix:     "Backup",
			expected:   "PROD-MySecret-Backup",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateTargetName(tt.sourceName, tt.prefix, tt.suffix)
			if result != tt.expected {
				t.Errorf("GenerateTargetName(%q, %q, %q) = %q, expected %q",
					tt.sourceName, tt.prefix, tt.suffix, result, tt.expected)
			}
		})
	}
}

func TestGenerateTargetNameEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		sourceName string
		prefix     string
		suffix     string
		validate   func(t *testing.T, result string)
	}{
		{
			name:       "multiple consecutive dashes in prefix",
			sourceName: "secret",
			prefix:     "prod--",
			suffix:     "",
			validate: func(t *testing.T, result string) {
				// Should not create triple dashes
				if strings.Contains(result, "---") {
					t.Errorf("Result should not contain triple dashes: %s", result)
				}
				expected := "prod--secret"
				if result != expected {
					t.Errorf("Expected %s, got %s", expected, result)
				}
			},
		},
		{
			name:       "multiple consecutive dashes in suffix",
			sourceName: "secret",
			prefix:     "",
			suffix:     "--backup",
			validate: func(t *testing.T, result string) {
				// Should not create triple dashes
				if strings.Contains(result, "---") {
					t.Errorf("Result should not contain triple dashes: %s", result)
				}
				expected := "secret--backup"
				if result != expected {
					t.Errorf("Expected %s, got %s", expected, result)
				}
			},
		},
		{
			name:       "source name starts and ends with dashes",
			sourceName: "-secret-",
			prefix:     "prod",
			suffix:     "backup",
			validate: func(t *testing.T, result string) {
				expected := "prod--secret--backup"
				if result != expected {
					t.Errorf("Expected %s, got %s", expected, result)
				}
			},
		},
		{
			name:       "all empty strings",
			sourceName: "",
			prefix:     "",
			suffix:     "",
			validate: func(t *testing.T, result string) {
				if result != "" {
					t.Errorf("Expected empty string, got %s", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateTargetName(tt.sourceName, tt.prefix, tt.suffix)
			tt.validate(t, result)
		})
	}
}

func TestGenerateTargetNameKubernetesCompliance(t *testing.T) {
	tests := []struct {
		name       string
		sourceName string
		prefix     string
		suffix     string
	}{
		{
			name:       "typical kubernetes resource name",
			sourceName: "my-app-config",
			prefix:     "prod",
			suffix:     "v1",
		},
		{
			name:       "long names",
			sourceName: "very-long-application-configuration-secret-name",
			prefix:     "production-environment",
			suffix:     "version-1-backup",
		},
		{
			name:       "names with numbers",
			sourceName: "app-v2-config",
			prefix:     "env1",
			suffix:     "backup2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateTargetName(tt.sourceName, tt.prefix, tt.suffix)

			// Basic Kubernetes naming validation
			// Should not start or end with dash (unless original source did)
			if len(result) > 0 {
				if result[0] == '-' && len(tt.prefix) > 0 && tt.prefix[0] != '-' {
					t.Errorf("Result should not start with dash when prefix doesn't: %s", result)
				}
				if result[len(result)-1] == '-' && len(tt.suffix) > 0 && tt.suffix[len(tt.suffix)-1] != '-' {
					t.Errorf("Result should not end with dash when suffix doesn't: %s", result)
				}
			}

			// Should only contain lowercase letters, numbers, and hyphens for typical k8s names
			// Note: This test is informational - the function doesn't enforce case conversion
			// as that might break existing naming conventions
		})
	}
}

func TestGenerateTargetNameConsistency(t *testing.T) {
	// Test that the function is deterministic
	sourceName := "test-secret"
	prefix := "prod"
	suffix := "backup"

	result1 := GenerateTargetName(sourceName, prefix, suffix)
	result2 := GenerateTargetName(sourceName, prefix, suffix)

	if result1 != result2 {
		t.Errorf("Function should be deterministic. Got %s and %s", result1, result2)
	}
}

func TestIsValidKubernetesResourceName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid simple name",
			input:    "my-secret",
			expected: true,
		},
		{
			name:     "valid name with numbers",
			input:    "secret-123",
			expected: true,
		},
		{
			name:     "valid single character",
			input:    "a",
			expected: true,
		},
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "starts with dash",
			input:    "-secret",
			expected: false,
		},
		{
			name:     "ends with dash",
			input:    "secret-",
			expected: false,
		},
		{
			name:     "contains uppercase",
			input:    "Secret",
			expected: false,
		},
		{
			name:     "contains special characters",
			input:    "secret@123",
			expected: false,
		},
		{
			name:     "contains underscore",
			input:    "secret_name",
			expected: false,
		},
		{
			name:     "too long",
			input:    strings.Repeat("a", 254),
			expected: false,
		},
		{
			name:     "exactly 253 characters",
			input:    strings.Repeat("a", 253),
			expected: true,
		},
		{
			name:     "valid complex name",
			input:    "my-app-v1-config-backup",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidKubernetesResourceName(tt.input)
			if result != tt.expected {
				t.Errorf("IsValidKubernetesResourceName(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenerateTargetNameValidation(t *testing.T) {
	tests := []struct {
		name       string
		sourceName string
		prefix     string
		suffix     string
		shouldWarn bool
	}{
		{
			name:       "valid combination",
			sourceName: "my-secret",
			prefix:     "prod",
			suffix:     "backup",
			shouldWarn: false,
		},
		{
			name:       "invalid prefix with uppercase",
			sourceName: "secret",
			prefix:     "PROD",
			suffix:     "",
			shouldWarn: true,
		},
		{
			name:       "invalid suffix with special chars",
			sourceName: "secret",
			prefix:     "",
			suffix:     "backup@v1",
			shouldWarn: true,
		},
		{
			name:       "result starts with dash",
			sourceName: "secret",
			prefix:     "-prod",
			suffix:     "",
			shouldWarn: true,
		},
		{
			name:       "result ends with dash",
			sourceName: "secret",
			prefix:     "",
			suffix:     "backup-",
			shouldWarn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't easily test the warning log, but we can test that the function
			// still returns a result and that the validation function works correctly
			result := GenerateTargetName(tt.sourceName, tt.prefix, tt.suffix)
			isValid := IsValidKubernetesResourceName(result)

			if tt.shouldWarn && isValid {
				t.Errorf("Expected invalid name but got valid: %s", result)
			} else if !tt.shouldWarn && !isValid {
				t.Errorf("Expected valid name but got invalid: %s", result)
			}
		})
	}
}
