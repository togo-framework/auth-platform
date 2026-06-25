package authplatform

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// subjectKey is the context key other plugins (auth) use for the current user id.
// We read several common spots so we work standalone or with auth.
func subjectID(r *http.Request) string {
	if v, ok := r.Context().Value(subjKey).(string); ok && v != "" {
		return v
	}
	return r.Header.Get("X-User-Id")
}

type subjCtx int

const subjKey subjCtx = 1

// WithSubject scopes a context to a user id (for tests / manual wiring).
func WithSubject(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, subjKey, userID)
}

// ResolveOrg is middleware that derives the current org from (in order) the
// X-Org-Id header, a `?org=` query param, or the first label of the host
// (subdomain) matched against an org slug, and stores it in the context.
func (s *Service) ResolveOrg(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orgID := r.Header.Get("X-Org-Id")
		if orgID == "" {
			orgID = r.URL.Query().Get("org")
		}
		if orgID == "" {
			if host := r.Host; strings.Count(host, ".") >= 2 {
				sub := host[:strings.Index(host, ".")]
				if o, ok := s.OrgBySlug(sub); ok {
					orgID = o.ID
				}
			}
		}
		if orgID != "" {
			r = r.WithContext(WithOrg(r.Context(), orgID))
		}
		next.ServeHTTP(w, r)
	})
}

// RequireOrgRole is middleware that rejects (403) a request whose subject is not
// a member of the current org with at least `role`.
func (s *Service) RequireOrgRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			orgID := OrgID(r.Context())
			if orgID == "" { // fall back to the header when ResolveOrg didn't run
				orgID = r.Header.Get("X-Org-Id")
			}
			uid := subjectID(r)
			if orgID == "" || uid == "" || !s.HasRole(orgID, uid, role) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Service) mountRoutes(r chi.Router) {
	r.Route("/api/orgs", func(r chi.Router) {
		// List the orgs the current user belongs to (org switcher).
		r.Get("/", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, 200, s.OrgsForUser(subjectID(req)))
		})
		// Create an org owned by the current user.
		r.Post("/", func(w http.ResponseWriter, req *http.Request) {
			var in struct{ Name, Slug string }
			_ = json.NewDecoder(req.Body).Decode(&in)
			org, err := s.CreateOrg(in.Name, in.Slug, subjectID(req))
			if err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 201, org)
		})
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", func(w http.ResponseWriter, req *http.Request) {
				if o, ok := s.GetOrg(chi.URLParam(req, "id")); ok {
					writeJSON(w, 200, o)
				} else {
					writeJSON(w, 404, map[string]string{"error": "not found"})
				}
			})
			r.Patch("/", func(w http.ResponseWriter, req *http.Request) {
				id := chi.URLParam(req, "id")
				var in struct {
					Branding *Branding      `json:"branding"`
					Settings map[string]any `json:"settings"`
				}
				_ = json.NewDecoder(req.Body).Decode(&in)
				if in.Branding != nil {
					_ = s.SetBranding(id, *in.Branding)
				}
				for k, v := range in.Settings {
					_ = s.SetSetting(id, k, v)
				}
				if o, ok := s.GetOrg(id); ok {
					writeJSON(w, 200, o)
				} else {
					writeJSON(w, 404, map[string]string{"error": "not found"})
				}
			})
			r.Delete("/", func(w http.ResponseWriter, req *http.Request) {
				s.DeleteOrg(chi.URLParam(req, "id"))
				w.WriteHeader(204)
			})
			r.Get("/members", func(w http.ResponseWriter, req *http.Request) {
				writeJSON(w, 200, s.Members(chi.URLParam(req, "id")))
			})
			r.Post("/invites", func(w http.ResponseWriter, req *http.Request) {
				var in struct{ Email, Role string }
				_ = json.NewDecoder(req.Body).Decode(&in)
				inv, err := s.Invite(chi.URLParam(req, "id"), in.Email, in.Role)
				if err != nil {
					writeJSON(w, 404, map[string]string{"error": err.Error()})
					return
				}
				writeJSON(w, 201, inv)
			})
		})
	})
	// Accept an invite: POST /api/org-invites/accept {token}
	r.Post("/api/org-invites/accept", func(w http.ResponseWriter, req *http.Request) {
		var in struct{ Token string }
		_ = json.NewDecoder(req.Body).Decode(&in)
		m, err := s.Accept(in.Token, subjectID(req))
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, m)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
