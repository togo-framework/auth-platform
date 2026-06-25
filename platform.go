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
// store by default; back it with a database via WithStore) and exposes a Go API
// plus a REST surface under /api/orgs. It composes with `auth` but works standalone.
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

// Store is the persistence seam. The default is a bounded in-memory store;
// install a DB-backed implementation with Service.WithStore. The Service always
// persists mutations via an explicit Save*, so a DB store never needs to track
// in-place changes to returned structs.
type Store interface {
	SaveOrg(o *Org)
	GetOrg(id string) (*Org, bool)
	OrgBySlug(slug string) (*Org, bool)
	AllOrgs() []*Org
	DeleteOrg(id string)

	SaveMember(m *Member)
	GetMember(orgID, userID string) (*Member, bool)
	MembersByOrg(orgID string) []*Member
	RemoveMember(orgID, userID string)
	OrgsForUser(userID string) []*Org

	SaveInvite(inv *Invite)
	GetInvite(tokenStr string) (*Invite, bool)
	DeleteInvite(tokenStr string)
}

// Service is the auth-platform runtime stored on the kernel (k.Get("auth-platform")).
type Service struct {
	store Store
}

func newService() *Service { return &Service{store: newMemStore()} }

// WithStore swaps the backing store (e.g. a DB-backed implementation).
func (s *Service) WithStore(store Store) *Service {
	s.store = store
	return s
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
	org := &Org{
		ID:        token()[:16],
		Name:      name,
		Slug:      slug,
		OwnerID:   ownerID,
		Branding:  Branding{Name: name},
		Settings:  map[string]any{},
		CreatedAt: time.Now().UTC(),
	}
	s.store.SaveOrg(org)
	s.store.SaveMember(&Member{OrgID: org.ID, UserID: ownerID, Role: RoleOwner, Status: StatusActive, JoinedAt: org.CreatedAt})
	return org, nil
}

// GetOrg returns an org by id.
func (s *Service) GetOrg(id string) (*Org, bool) { return s.store.GetOrg(id) }

// OrgBySlug returns an org by slug.
func (s *Service) OrgBySlug(slug string) (*Org, bool) { return s.store.OrgBySlug(slug) }

// AllOrgs lists every org (admin view).
func (s *Service) AllOrgs() []*Org {
	out := s.store.AllOrgs()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// DeleteOrg removes an org and its memberships.
func (s *Service) DeleteOrg(id string) { s.store.DeleteOrg(id) }

// Invite creates a pending invitation and returns its token.
func (s *Service) Invite(orgID, email, role string) (*Invite, error) {
	if role == "" {
		role = RoleMember
	}
	if _, ok := s.store.GetOrg(orgID); !ok {
		return nil, ErrNotFound
	}
	inv := &Invite{Token: token(), OrgID: orgID, Email: strings.ToLower(email), Role: role, CreatedAt: time.Now().UTC()}
	s.store.SaveInvite(inv)
	return inv, nil
}

// Accept consumes an invite token and adds the user as an active member.
func (s *Service) Accept(inviteToken, userID string) (*Member, error) {
	inv, ok := s.store.GetInvite(inviteToken)
	if !ok {
		return nil, ErrInviteUsed
	}
	s.store.DeleteInvite(inviteToken)
	if _, ok := s.store.GetOrg(inv.OrgID); !ok {
		return nil, ErrNotFound
	}
	m := &Member{OrgID: inv.OrgID, UserID: userID, Role: inv.Role, Status: StatusActive, JoinedAt: time.Now().UTC()}
	s.store.SaveMember(m)
	return m, nil
}

// AddMember directly adds (or updates) a member.
func (s *Service) AddMember(orgID, userID, role string) (*Member, error) {
	if _, ok := s.store.GetOrg(orgID); !ok {
		return nil, ErrNotFound
	}
	m := &Member{OrgID: orgID, UserID: userID, Role: role, Status: StatusActive, JoinedAt: time.Now().UTC()}
	s.store.SaveMember(m)
	return m, nil
}

// RemoveMember removes a member from an org.
func (s *Service) RemoveMember(orgID, userID string) { s.store.RemoveMember(orgID, userID) }

// SetRole changes a member's role.
func (s *Service) SetRole(orgID, userID, role string) error {
	m, ok := s.store.GetMember(orgID, userID)
	if !ok {
		return ErrNotFound
	}
	m.Role = role
	s.store.SaveMember(m)
	return nil
}

// Members lists an org's members.
func (s *Service) Members(orgID string) []*Member {
	out := s.store.MembersByOrg(orgID)
	sort.Slice(out, func(i, j int) bool { return out[i].JoinedAt.Before(out[j].JoinedAt) })
	return out
}

// MemberRole returns a user's role in an org (and whether they're a member).
func (s *Service) MemberRole(orgID, userID string) (string, bool) {
	if m, ok := s.store.GetMember(orgID, userID); ok {
		return m.Role, true
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
	out := s.store.OrgsForUser(userID)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// SetSetting sets a per-org setting.
func (s *Service) SetSetting(orgID, key string, value any) error {
	o, ok := s.store.GetOrg(orgID)
	if !ok {
		return ErrNotFound
	}
	if o.Settings == nil {
		o.Settings = map[string]any{}
	}
	o.Settings[key] = value
	s.store.SaveOrg(o)
	return nil
}

// Setting reads a per-org setting.
func (s *Service) Setting(orgID, key string) (any, bool) {
	if o, ok := s.store.GetOrg(orgID); ok {
		v, ok := o.Settings[key]
		return v, ok
	}
	return nil, false
}

// SetBranding updates an org's branding.
func (s *Service) SetBranding(orgID string, b Branding) error {
	o, ok := s.store.GetOrg(orgID)
	if !ok {
		return ErrNotFound
	}
	o.Branding = b
	s.store.SaveOrg(o)
	return nil
}

// ── in-memory store (default) ──

type memStore struct {
	mu      sync.RWMutex
	orgs    map[string]*Org
	members map[string]map[string]*Member // orgID -> userID -> member
	invites map[string]*Invite            // token -> invite
}

func newMemStore() *memStore {
	return &memStore{
		orgs:    map[string]*Org{},
		members: map[string]map[string]*Member{},
		invites: map[string]*Invite{},
	}
}

func (m *memStore) SaveOrg(o *Org) {
	m.mu.Lock()
	m.orgs[o.ID] = o
	m.mu.Unlock()
}

func (m *memStore) GetOrg(id string) (*Org, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.orgs[id]
	return o, ok
}

func (m *memStore) OrgBySlug(slug string) (*Org, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, o := range m.orgs {
		if o.Slug == slug {
			return o, true
		}
	}
	return nil, false
}

func (m *memStore) AllOrgs() []*Org {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Org, 0, len(m.orgs))
	for _, o := range m.orgs {
		out = append(out, o)
	}
	return out
}

func (m *memStore) DeleteOrg(id string) {
	m.mu.Lock()
	delete(m.orgs, id)
	delete(m.members, id)
	m.mu.Unlock()
}

func (m *memStore) SaveMember(mem *Member) {
	m.mu.Lock()
	if m.members[mem.OrgID] == nil {
		m.members[mem.OrgID] = map[string]*Member{}
	}
	m.members[mem.OrgID][mem.UserID] = mem
	m.mu.Unlock()
}

func (m *memStore) GetMember(orgID, userID string) (*Member, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if mm := m.members[orgID]; mm != nil {
		if mem := mm[userID]; mem != nil {
			return mem, true
		}
	}
	return nil, false
}

func (m *memStore) MembersByOrg(orgID string) []*Member {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mm := m.members[orgID]
	out := make([]*Member, 0, len(mm))
	for _, mem := range mm {
		out = append(out, mem)
	}
	return out
}

func (m *memStore) RemoveMember(orgID, userID string) {
	m.mu.Lock()
	if mm := m.members[orgID]; mm != nil {
		delete(mm, userID)
	}
	m.mu.Unlock()
}

func (m *memStore) OrgsForUser(userID string) []*Org {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Org
	for orgID, mm := range m.members {
		if mm[userID] != nil {
			if o := m.orgs[orgID]; o != nil {
				out = append(out, o)
			}
		}
	}
	return out
}

func (m *memStore) SaveInvite(inv *Invite) {
	m.mu.Lock()
	m.invites[inv.Token] = inv
	m.mu.Unlock()
}

func (m *memStore) GetInvite(tokenStr string) (*Invite, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inv, ok := m.invites[tokenStr]
	return inv, ok
}

func (m *memStore) DeleteInvite(tokenStr string) {
	m.mu.Lock()
	delete(m.invites, tokenStr)
	m.mu.Unlock()
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
