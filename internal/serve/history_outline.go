package serve

import (
	"encoding/json"
	"net/http"
	"reasonix/internal/session"
	"strconv"
)

func (s *Server) sessionHistoryOutline(w http.ResponseWriter, r *http.Request) {
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	query, ref, ok := s.canonicalSessionQuery(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	req := session.HistoryOutlineRequest{Generation: values.Get("generation")}
	for key, target := range map[string]*int{"startTurn": &req.StartTurn, "limit": &req.Limit} {
		if raw := values.Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				http.Error(w, "invalid outline range", http.StatusBadRequest)
				return
			}
			*target = value
		}
	}
	if raw := values.Get("snapshotSequence"); raw != "" {
		cut, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			http.Error(w, "invalid snapshot sequence", http.StatusBadRequest)
			return
		}
		req.SnapshotSequence = &cut
	}
	page, err := query.ReadHistoryOutline(r.Context(), ref, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}
