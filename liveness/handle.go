package liveness

import (
	"encoding/json"
	"fmt"
	"github.com/mittwald/kubernetes-replicator/replicate/common"
	"net/http"
)

type response struct {
	NotReady []string `json:"notReady"`
}

// Handler implements a HTTP response handler that reports on the current
// liveness status of the controller
type Handler struct {
	Replicators []common.Replicator
}

func (h *Handler) notReadyComponents() []string {
	notReady := make([]string, 0)

	for i := range h.Replicators {
		synced := h.Replicators[i].Synced()

		if !synced {
			notReady = append(notReady, fmt.Sprintf("%T", h.Replicators[i]))
		}
	}

	return notReady
}

// noinspection GoUnusedParameter
func (h *Handler) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/healthz" {
		res.WriteHeader(http.StatusOK)
	} else {
		r := response{
			NotReady: h.notReadyComponents(),
		}

		if len(r.NotReady) > 0 {
			res.WriteHeader(http.StatusServiceUnavailable)
		} else {
			res.WriteHeader(http.StatusOK)
		}
		enc := json.NewEncoder(res)
		_ = enc.Encode(&r)
	}
}
