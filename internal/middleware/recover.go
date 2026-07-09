package middleware

import (
	"net/http"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/logging"
)

// RecoverJSON recovers panics and returns the gateway's JSON error envelope.
func RecoverJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logging.FromContext(r.Context()).Error("panic recovered", "panic", recovered)
				apierror.WriteOpenAI(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
