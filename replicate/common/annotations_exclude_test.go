package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnnotationsFilterShouldExclude(t *testing.T) {
	tests := []struct {
		patterns    []string
		annotations map[string]string
		expectedRes bool
	}{
		{
			patterns: []string{"vcluster.loft.sh/"},
			annotations: map[string]string{
				"vcluster.loft.sh/any1": "any1",
				"vcluster.loft.sh/any2": "any2",
			},
			expectedRes: true,
		},
		{
			patterns: []string{"vcluster.loft.sh/"},
			annotations: map[string]string{
				"any1": "any1",
				"any2": "any2",
			},
			expectedRes: false,
		},
		{
			patterns: []string{},
			annotations: map[string]string{
				"any1": "any1",
				"any2": "any2",
			},
			expectedRes: false,
		},
		{
			patterns:    []string{"vcluster.loft.sh/"},
			annotations: map[string]string{},
			expectedRes: false,
		},
	}

	for _, test := range tests {
		annotationsFilter := NewAnnotationsFilter(test.patterns)

		res := annotationsFilter.ShouldExclude(test.annotations)

		assert.Equal(t, test.expectedRes, res)
	}
}
