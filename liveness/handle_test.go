package liveness

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mittwald/kubernetes-replicator/replicate/common"
	v1 "k8s.io/api/core/v1"

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

// noinspection GoUnusedParameter
func (r *MockReplicator) NamespaceAdded(ns *v1.Namespace) {
	// Do nothing
}

func (r *MockReplicator) GetKind() string {
	return ""
}

func buildReqRes(t *testing.T) (*http.Request, *httptest.ResponseRecorder) {
	req, err := http.NewRequest("GET", "/status", nil)
	res := httptest.NewRecorder()

	assert.Nil(t, err)
	return req, res
}

func TestReturns200IfAllReplicatorsAreSynced(t *testing.T) {
	req, res := buildReqRes(t)

	handler := Handler{
		Replicators: []common.Replicator{
			&MockReplicator{synced: true},
			&MockReplicator{synced: true},
		},
	}

	handler.ServeHTTP(res, req)

	assert.Equal(t, http.StatusOK, res.Code)
}

func TestReturns503IfOneReplicatorIsNotSynced(t *testing.T) {
	req, res := buildReqRes(t)

	handler := Handler{
		Replicators: []common.Replicator{
			&MockReplicator{synced: true},
			&MockReplicator{synced: false},
		},
		notReady: []string{"replicator2"},
	}

	handler.ServeHTTP(res, req)

	assert.Equal(t, http.StatusServiceUnavailable, res.Code)
}
