package fbhttp

import "net/http"

// The application still needs inline bootstrap/legacy scripts and external
// editor/recaptcha resources. Apply navigation/framing restrictions to its HTML
// without pretending it already has an effective script-src XSS policy.
const applicationCSP = "base-uri 'self'; form-action 'self'; frame-ancestors 'self';"

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep the existing stricter policy on API responses and user content.
		// SAMEORIGIN permits the application's embedded PDF previews.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; "+applicationCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}
