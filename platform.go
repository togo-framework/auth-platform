// Package authplatform adds organizations / teams (multi-tenancy) on top of the
// togo auth plugin — the layer Fort calls "platforms" and Laravel calls "teams".
//
// An Org groups users; each user is a Member with a per-org Role (owner / admin
// / member or a custom role). Users are invited by email and accept via a token.
// A request is scoped to a "current org" (resolved from the X-Org-Id header, a
// subdomain, or a JWT claim) so the rest of the app can read OrgID(ctx). Each org
// carries its own Settings and Branding.
//
// The plugin owns its data through a small Store interface (a bounded in-memory
// store by default; back it with a database later) and exposes a Go API plus a
// REST surface under /api/orgs. It composes with `auth` but works standalone.
package authplatform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/togo-framework/togo"
)

// Built-in roles (custom role strings are allowed too). Ranked for RequireOrgRole.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Member status values.
const (
	StatusInvited = "invited"
	StatusActive  = "active"
)

var roleRank = map[string]int{RoleMember: 1, RoleAdmin: 2, RoleOwner: 3}

// Errors.
var (
	ErrNotFound   = errors.New("authplatform: not found")
	ErrForbidden  = errors.New("authplatform: forbidden")
	ErrInviteUsed = errors.New("authplatform: invite already used or expired")
)

// Branding is per-org white-label config.
type Branding struct {
	Name         string `json:"name,omitempty"`
	PrimaryColor string `json:"primary_color,omitempty"`
	AccentColor  string `json:"accent_color,omitempty"`
	LogoURL      string `json:"logo_url,omitempty"`
}

// Org is a tenant (organization / team).
type Org struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Slug      string         `json:"slug"`
	OwnerID   string         `json:"owner_id"`
	Branding  Branding       `json:"branding"`
	Settings  map[string]any `json:"settings"`
	CreatedAt time.Time      `json:"created_at"`
}

// Member is a user's membership in an org.
type Member struct {
	OrgID    string    `json:"org_id"`
	UserID   string    `json:"user_id"`
	Role     string    `json:"role"`
	Status   string    `json:"status"`
	JoinedAt time.Time `json:"joined_at"`
}

// Invite is a pending invitation by email.
type Invite struct {
	Token     string    `json:"token"`
	OrgID     string    `json:"org_id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// Service is the auth-platform runtime stored on the kernel (k.Get("auth-platform")).
type Service struct {
	mu      sync.RWMutex
	orgs    map[string]*Org
	members map[string]map[string]*Member // orgID -> userID -> member
	invites map[string]*Invite            // token -> invite
}

func newService() *Service {
	return &Service{
		orgs:    map[string]*Org{},
		members: map[string]map[string]*Member{},
		invites: map[string]*Invite{},
	}
}

func init() {
	togo.RegisterProviderFunc("auth-platform", togo.PriorityLate+10, func(k *togo.Kernel) error {
		s := newService()
		k.Set("auth-platform", s)
		if k.Router != nil {
			s.mountRoutes(k.Router)
		}
		return nil
	})
}

// FromKernel returns the auth-platform Service registered on the kernel.
func FromKernel(k *togo.Kernel) (*Service, bool) {
	v, ok := k.Get("auth-platform")
	if !ok {
		return nil, false
	}
	s, ok := v.(*Service)
	return s, ok
}

func token() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == ' ' || r == '_' || r == '-':
			return '-'
		default:
			return -1
		}
	}, s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// CreateOrg creates an org owned by ownerID and adds the owner as an active member.
func (s *Service) CreateOrg(name, slug, ownerID string) (*Org, error) {
	if name == "" || ownerID == "" {
		return nil, errors.New("authplatform: name and ownerID are required")
	}
	if slug == "" {
		slug = slugify(name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	org := &Org{
		ID:        token()[:16],
		Name:      name,
		Slug:      slug,
		OwnerID:   ownerID,
		Branding:  Branding{Name: name},
		Settings:  map[string]any{},
		CreatedAt: time.Now().UTC(),
	}
	s.orgs[org.ID] = org
	s.members[org.ID] = map[string]*Member{
		ownerID: {OrgID: org.ID, UserID: ownerID, Role: RoleOwner, Status: StatusActive, JoinedAt: org.CreatedAt},
	}
	return org, nil
}

// GetOrg returns an org by id.
func (s *Service) GetOrg(id string) (*Org, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.orgs[id]
	return o, ok
}

// OrgBySlug returns an org by slug.
func (s *Service) OrgBySlug(slug string) (*Org, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.orgs {
		if o.Slug == slug {
			return o, true
		}
	}
	return nil, false
}

// DeleteOrg removes an org and its memberships.
func (s *Service) DeleteOrg(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.orgs, id)
	delete(s.members, id)
}

// Invite creates a pending invitation and returns its token.
func (s *Service) Invite(orgID, email, role string) (*Invite, error) {
	if role == "" {
		role = RoleMember
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[orgID]; !ok {
		return nil, ErrNotFound
	}
	inv := &Invite{Token: token(), OrgID: orgID, Email: strings.ToLower(email), Role: role, CreatedAt: time.Now().UTC()}
	s.invites[inv.Token] = inv
	return inv, nil
}

// Accept consumes an invite token and adds the user as an active member.
func (s *Service) Accept(inviteToken, userID string) (*Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.invites[inviteToken]
	if !ok {
		return nil, ErrInviteUsed
	}
	delete(s.invites, inviteToken)
	if _, ok := s.orgs[inv.OrgID]; !ok {
		return nil, ErrNotFound
	}
	m := &Member{OrgID: inv.OrgID, UserID: userID, Role: inv.Role, Status: StatusActive, JoinedAt: time.Now().UTC()}
	if s.members[inv.OrgID] == nil {
		s.members[inv.OrgID] = map[string]*Member{}
	}
	s.members[inv.OrgID][userID] = m
	return m, nil
}

// AddMember directly adds (or updates) a member.
func (s *Service) AddMember(orgID, userID, role string) (*Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[orgID]; !ok {
		return nil, ErrNotFound
	}
	if s.members[orgID] == nil {
		s.members[orgID] = map[string]*Member{}
	}
	m := &Member{OrgID: orgID, UserID: userID, Role: role, Status: StatusActive, JoinedAt: time.Now().UTC()}
	s.members[orgID][userID] = m
	return m, nil
}

// RemoveMember removes a member from an org.
func (s *Service) RemoveMember(orgID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mm := s.members[orgID]; mm != nil {
		delete(mm, userID)
	}
}

// SetRole changes a member's role.
func (s *Service) SetRole(orgID, userID, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mm := s.members[orgID]
	if mm == nil || mm[userID] == nil {
		return ErrNotFound
	}
	mm[userID].Role = role
	return nil
}

// Members lists an org's members.
func (s *Service) Members(orgID string) []*Member {
	s.mu.RLock()
	defer s.mu.RUnlock()
	mm := s.members[orgID]
	out := make([]*Member, 0, len(mm))
	for _, m := range mm {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JoinedAt.Before(out[j].JoinedAt) })
	return out
}

// MemberRole returns a user's role in an org (and whether they're a member).
func (s *Service) MemberRole(orgID, userID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if mm := s.members[orgID]; mm != nil {
		if m := mm[userID]; m != nil {
			return m.Role, true
		}
	}
	return "", false
}

// HasRole reports whether the user's role in the org is at least `role` (by rank;
// custom roles match by exact name).
func (s *Service) HasRole(orgID, userID, role string) bool {
	have, ok := s.MemberRole(orgID, userID)
	if !ok {
		return false
	}
	want, known := roleRank[role]
	got, hk := roleRank[have]
	if known && hk {
		return got >= want
	}
	return have == role
}

// OrgsForUser lists every org a user belongs to (the org switcher feed).
func (s *Service) OrgsForUser(userID string) []*Org {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Org
	for orgID, mm := range s.members {
		if mm[userID] != nil {
			if o := s.orgs[orgID]; o != nil {
				out = append(out, o)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// SetSetting sets a per-org setting.
func (s *Service) SetSetting(orgID, key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[orgID]
	if !ok {
		return ErrNotFound
	}
	if o.Settings == nil {
		o.Settings = map[string]any{}
	}
	o.Settings[key] = value
	return nil
}

// Setting reads a per-org setting.
func (s *Service) Setting(orgID, key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if o, ok := s.orgs[orgID]; ok {
		v, ok := o.Settings[key]
		return v, ok
	}
	return nil, false
}

// SetBranding updates an org's branding.
func (s *Service) SetBranding(orgID string, b Branding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[orgID]
	if !ok {
		return ErrNotFound
	}
	o.Branding = b
	return nil
}

// ── request context ──

type ctxKey int

const orgCtxKey ctxKey = 0

// WithOrg returns a context scoped to orgID.
func WithOrg(ctx context.Context, orgID string) context.Context {
	return context.WithValue(ctx, orgCtxKey, orgID)
}

// OrgID returns the current org id from the context ("" if unset).
func OrgID(ctx context.Context) string {
	if v, ok := ctx.Value(orgCtxKey).(string); ok {
		return v
	}
	return ""
}

// CurrentOrg returns the current Org from the context.
func (s *Service) CurrentOrg(ctx context.Context) (*Org, bool) {
	id := OrgID(ctx)
	if id == "" {
		return nil, false
	}
	return s.GetOrg(id)
}
