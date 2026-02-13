package common

import (
	"strings"
	"testing"
)

func TestAnnotationConstants(t *testing.T) {
	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{
			name:     "PrefixAnnotation has correct value",
			constant: PrefixAnnotation,
			expected: "replicator.v1.mittwald.de/prefix",
		},
		{
			name:     "SuffixAnnotation has correct value",
			constant: SuffixAnnotation,
			expected: "replicator.v1.mittwald.de/suffix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.constant != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, tt.constant)
			}
		})
	}
}

func TestAnnotationConstantsFormat(t *testing.T) {
	annotations := []string{
		PrefixAnnotation,
		SuffixAnnotation,
	}

	for _, annotation := range annotations {
		t.Run("annotation format validation for "+annotation, func(t *testing.T) {
			// Should start with replicator.v1.mittwald.de/
			if !strings.HasPrefix(annotation, "replicator.v1.mittwald.de/") {
				t.Errorf("Annotation %s should start with 'replicator.v1.mittwald.de/'", annotation)
			}

			// Should not contain uppercase letters
			if strings.ToLower(annotation) != annotation {
				t.Errorf("Annotation %s should be lowercase", annotation)
			}

			// Should not end with slash
			if strings.HasSuffix(annotation, "/") {
				t.Errorf("Annotation %s should not end with slash", annotation)
			}

			// Should not contain spaces
			if strings.Contains(annotation, " ") {
				t.Errorf("Annotation %s should not contain spaces", annotation)
			}
		})
	}
}

func TestAllAnnotationConstantsUnique(t *testing.T) {
	annotations := []string{
		ReplicateFromAnnotation,
		ReplicatedAtAnnotation,
		ReplicatedFromVersionAnnotation,
		ReplicatedKeysAnnotation,
		ReplicationAllowed,
		ReplicationAllowedNamespaces,
		ReplicateTo,
		ReplicateToMatching,
		KeepOwnerReferences,
		StripLabels,
		PrefixAnnotation,
		SuffixAnnotation,
	}

	seen := make(map[string]bool)
	for _, annotation := range annotations {
		if seen[annotation] {
			t.Errorf("Duplicate annotation constant found: %s", annotation)
		}
		seen[annotation] = true
	}

	// Verify we have the expected number of unique annotations
	expectedCount := 12
	if len(seen) != expectedCount {
		t.Errorf("Expected %d unique annotations, got %d", expectedCount, len(seen))
	}
}
