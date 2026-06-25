package authplatform

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateOrgAddsOwnerMembership(t *testing.T) {
	s := newService()
	org, err := s.CreateOrg("Acme Inc", "", "user-owner")
	if err != nil {
		t.Fatal(err)
	}
	if org.Slug != "acme-inc" {
		t.Errorf("slug = %q, want acme-inc", org.Slug)
	}
	role, ok := s.MemberRole(org.ID, "user-owner")
	if !ok || role != RoleOwner {
		t.Fatalf("owner membership = %q,%v; want owner,true", role, ok)
	}
	if got := s.Members(org.ID); len(got) != 1 {
		t.Fatalf("members = %d, want 1", len(got))
	}
}

func TestInviteAcceptAddsMember(t *testing.T) {
	s := newService()
	org, _ := s.CreateOrg("Beta", "", "owner")
	inv, err := s.Invite(org.ID, "jane@example.com", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Accept(inv.Token, "user-jane")
	if err != nil {
		t.Fatal(err)
	}
	if m.Role != RoleAdmin || m.Status != StatusActive {
		t.Errorf("member = %+v", m)
	}
	// Token is single-use.
	if _, err := s.Accept(inv.Token, "user-jane"); err == nil {
		t.Error("invite token should be single-use")
	}
}

func TestRoleGating(t *testing.T) {
	s := newService()
	org, _ := s.CreateOrg("Gamma", "", "owner")
	s.AddMember(org.ID, "member1", RoleMember)
	// Owner outranks admin/member.
	if !s.HasRole(org.ID, "owner", RoleAdmin) {
		t.Error("owner should satisfy admin requirement")
	}
	// Member does not satisfy admin.
	if s.HasRole(org.ID, "member1", RoleAdmin) {
		t.Error("member should NOT satisfy admin requirement")
	}
	// Non-member fails everything.
	if s.HasRole(org.ID, "stranger", RoleMember) {
		t.Error("non-member should not have any role")
	}
}

func TestRequireOrgRoleMiddleware(t *testing.T) {
	s := newService()
	org, _ := s.CreateOrg("Delta", "", "owner")
	s.AddMember(org.ID, "viewer", RoleMember)

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := s.RequireOrgRole(RoleAdmin)(ok)

	// Owner passes.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Org-Id", org.ID)
	req.Header.Set("X-User-Id", "owner")
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("owner got %d, want 200", rr.Code)
	}

	// Member (below admin) is forbidden.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Org-Id", org.ID)
	req.Header.Set("X-User-Id", "viewer")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("member got %d, want 403", rr.Code)
	}
}

func TestResolveOrgFromHeader(t *testing.T) {
	s := newService()
	org, _ := s.CreateOrg("Epsilon", "", "owner")

	var seen string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = OrgID(r.Context())
	})
	h := s.ResolveOrg(inner)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Org-Id", org.ID)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != org.ID {
		t.Errorf("resolved org = %q, want %q", seen, org.ID)
	}

	// Subdomain resolution.
	seen = ""
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "epsilon.app.example.com"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != org.ID {
		t.Errorf("subdomain resolved org = %q, want %q", seen, org.ID)
	}
}

func TestOrgSwitcherAndSettingsIsolation(t *testing.T) {
	s := newService()
	a, _ := s.CreateOrg("OrgA", "", "user1")
	b, _ := s.CreateOrg("OrgB", "", "user2")
	s.AddMember(b.ID, "user1", RoleMember) // user1 is in both

	orgs := s.OrgsForUser("user1")
	if len(orgs) != 2 {
		t.Fatalf("user1 belongs to %d orgs, want 2", len(orgs))
	}

	// Per-org settings are isolated.
	if err := s.SetSetting(a.ID, "theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if v, ok := s.Setting(a.ID, "theme"); !ok || v != "dark" {
		t.Errorf("OrgA theme = %v,%v", v, ok)
	}
	if _, ok := s.Setting(b.ID, "theme"); ok {
		t.Error("OrgB should not see OrgA's setting")
	}
}
