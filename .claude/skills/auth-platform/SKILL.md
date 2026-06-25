---
name: auth-platform
description: Add organizations/teams multi-tenancy to a togo app — create orgs, invite members with per-org roles, scope requests to the current org, and manage per-org settings & branding using the auth-platform plugin.
---

# togo auth-platform (orgs & teams)

Use the `togo-framework/auth-platform` plugin to make a togo app multi-tenant.

## Core API
```go
s, _ := authplatform.FromKernel(k)
org, _ := s.CreateOrg(name, slug, ownerID)      // creator = owner
inv, _ := s.Invite(org.ID, email, role)         // role: owner|admin|member|custom
s.Accept(inv.Token, userID)                      // single-use token
s.SetRole(org.ID, userID, role); s.RemoveMember(org.ID, userID)
s.HasRole(org.ID, userID, authplatform.RoleAdmin)
s.OrgsForUser(userID)                            // org switcher
s.SetSetting / s.Setting / s.SetBranding         // per-org config
```

## Request scoping
- `router.Use(s.ResolveOrg)` resolves the current org from `X-Org-Id`, `?org=`, or subdomain into the context.
- `authplatform.OrgID(ctx)` / `s.CurrentOrg(ctx)` read it.
- `router.With(s.RequireOrgRole(role))` gates a route (403 if the user lacks the role in the current org).

## Roles
`owner` > `admin` > `member` (ranked — a higher role satisfies a lower requirement). Custom roles match by exact name.

## Guidance
- Always scope tenant data by `OrgID(ctx)`; never trust a client-supplied org id without checking membership (`HasRole`).
- Make the owner non-removable; transfer ownership explicitly.
- Persist by backing the in-memory store with your database once the model stabilizes.
