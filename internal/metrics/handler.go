package metrics

import (
	"encoding/json"
	"net/http"
)

// Handler serves c's current Snapshot as JSON. Point curl or a debug
// dashboard at it — there's no scrape-format (Prometheus text exposition,
// etc.) support, since nothing in this project needs one yet.
func Handler(c *Counters) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(c.Snapshot())
	})
}
