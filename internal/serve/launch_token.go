package serve

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// launchTokenRequiredCode identifies a state-changing request refused for
// lacking the launch token while serve authentication is off.
const launchTokenRequiredCode = "launch_token_required"

// presentsLaunchToken reports whether r carries this launch's token, either as
// the cookie /auth/token issues or as an Authorization bearer.
func (ag *authGate) presentsLaunchToken(r *http.Request) bool {
	if ag.token == "" {
		return false
	}
	if c, err := r.Cookie(cookieToken); err == nil && tokenEqual(c.Value, ag.token) {
		return true
	}
	return tokenEqual(bearerToken(r.Header.Get("Authorization")), ag.token)
}

func bearerToken(header string) string {
	scheme, value, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}

func tokenEqual(got, want string) bool {
	got = strings.TrimSpace(got)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// refuseUntokenedMutation answers a state-changing request that lacks the
// launch token while authentication is off. Approvals require the launch token
// even then: whoever reaches the listener is not thereby the operator, and
// every mutation can widen what the agent may do, so the method decides.
func (ag *authGate) refuseUntokenedMutation(w http.ResponseWriter, r *http.Request) bool {
	if ag.mode != authNone {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	if ag.presentsLaunchToken(r) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":    launchTokenRequiredCode,
		"message": "approvals require the launch token",
	})
	return true
}

// mutationGate applies refuseUntokenedMutation inside the host and CSRF guards,
// so a request those refuse is still reported by the invariant that fired first.
func (ag *authGate) mutationGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ag.refuseUntokenedMutation(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}
