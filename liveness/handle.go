package liveness

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mittwald/kubernetes-replicator/replicate"
)

type response struct {
	NotReady []string `json:"notReady"`
}

// Handler implements a HTTP response handler that reports on the current
// liveness status of the controller
type Handler struct {
	Replicators []replicate.Replicator
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

func (h *Handler) ServeHTTP(res http.ResponseWriter, req *http.Request) {
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
