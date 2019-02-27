package liveness

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mittwald/kubernetes-replicator/replicate"

	"github.com/stretchr/testify/assert"
)

type MockReplicator struct {
	synced bool
}

func (r *MockReplicator) Run() {
}

func (r *MockReplicator) Synced() bool {
	return r.synced
}

func TestReturns200IfAllReplicatorsAreSynced(t *testing.T) {
	req, err := http.NewRequest("GET", "/status", nil)
	res := httptest.NewRecorder()

	assert.Nil(t, err)

	handler := Handler{
		Replicators: []replicate.Replicator{
			&MockReplicator{synced: true},
			&MockReplicator{synced: true},
		},
	}

	handler.ServeHTTP(res, req)

	assert.Equal(t, http.StatusOK, res.Code)
}

func TestReturns503IfOneReplicatorIsNotSynced(t *testing.T) {
	req, err := http.NewRequest("GET", "/status", nil)
	res := httptest.NewRecorder()

	assert.Nil(t, err)

	handler := Handler{
		Replicators: []replicate.Replicator{
			&MockReplicator{synced: true},
			&MockReplicator{synced: false},
		},
	}

	handler.ServeHTTP(res, req)

	assert.Equal(t, http.StatusServiceUnavailable, res.Code)
}
