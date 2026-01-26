package liveness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mittwald/kubernetes-replicator/replicate/common"
)

type response struct {
	NotReady []string `json:"notReady"`
}

// Handler implements a HTTP response handler that reports on the current
// liveness status of the controller
type Handler struct {
	Replicators []common.Replicator
	SyncPeriod  time.Duration

	mu       sync.Mutex
	notReady []string
}

// RunSyncLoop starts an infinite synchronization loop of replicators
func (h *Handler) RunSyncLoop() {
	h.notReady = make([]string, 0)
	t := time.NewTicker(h.SyncPeriod)
	defer t.Stop()

	for {
		h.notReadyComponents()
		<-t.C
	}
}

func (h *Handler) notReadyComponents() {
	notReady := make([]string, 0)

	now := time.Now()
	for i := range h.Replicators {
		synced := h.checkReplicatorSynced(h.Replicators[i])

		if !synced {
			notReady = append(notReady, fmt.Sprintf("%T", h.Replicators[i]))
		}
	}
	duration := time.Since(now)

	h.mu.Lock()
	h.notReady = notReady
	h.mu.Unlock()

	log.Infof("Sum of sync durations for all replicators: %v ms", duration.Milliseconds())
}

func (h *Handler) checkReplicatorSynced(replicator common.Replicator) bool {
	var synced bool
	syncedChan := make(chan bool, 1)

	ctx, cancel := context.WithTimeout(context.Background(), h.SyncPeriod)
	defer cancel()

	now := time.Now()
	go func() {
		syncedChan <- replicator.Synced()
	}()

	select {
	case synced = <-syncedChan:
	case <-ctx.Done():
		synced = false
		log.Warnf("Timeout for sync replicator %s after %v", replicator.GetKind(), h.SyncPeriod)
	}

	duration := time.Since(now)

	log.Infof("Sync duration for replicator %s: %v ms", replicator.GetKind(), duration.Milliseconds())

	return synced
}

// noinspection GoUnusedParameter
func (h *Handler) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/healthz" {
		res.WriteHeader(http.StatusOK)
	} else {
		h.mu.Lock()
		r := response{
			NotReady: h.notReady,
		}
		h.mu.Unlock()

		if len(r.NotReady) > 0 {
			res.WriteHeader(http.StatusServiceUnavailable)
		} else {
			res.WriteHeader(http.StatusOK)
		}
		enc := json.NewEncoder(res)
		_ = enc.Encode(&r)
	}
}
