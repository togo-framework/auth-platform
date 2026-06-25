# auth-platform — usage

## Orgs & membership
```go
s, _ := authplatform.FromKernel(k)
org, _ := s.CreateOrg("Acme Inc", "", ownerID) // creator becomes owner
s.AddMember(org.ID, userID, authplatform.RoleMember)
s.SetRole(org.ID, userID, authplatform.RoleAdmin)
s.Members(org.ID)
```

## Invites
```go
inv, _ := s.Invite(org.ID, "jane@acme.com", authplatform.RoleAdmin) // returns a token
m, _ := s.Accept(inv.Token, janeUserID)                              // single-use
```

## Roles
`owner` > `admin` > `member` (ranked). `HasRole(orgID, userID, role)` is satisfied by any higher rank. Custom roles match by exact name.

## Request scoping
```go
router.Use(s.ResolveOrg)                 // X-Org-Id header, ?org=, or subdomain → context
orgID := authplatform.OrgID(ctx)
org, _ := s.CurrentOrg(ctx)
router.With(s.RequireOrgRole(authplatform.RoleAdmin)).Post("/api/x", h) // 403 if below
```

## Settings & branding (per org)
```go
s.SetSetting(org.ID, "feature.beta", true)
v, ok := s.Setting(org.ID, "feature.beta")
s.SetBranding(org.ID, authplatform.Branding{PrimaryColor: "#2C7BE2", LogoURL: "/logo.svg"})
```

## Org switcher
```go
orgs := s.OrgsForUser(userID) // every org the user belongs to
```

## REST
`GET/POST /api/orgs`, `GET/PATCH/DELETE /api/orgs/{id}`, `GET /api/orgs/{id}/members`, `POST /api/orgs/{id}/invites`, `POST /api/org-invites/accept`. Current user comes from the `auth` context or `X-User-Id`.
