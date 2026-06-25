<div align="center">
  <img src=".github/assets/togo-mark.svg" alt="togo" height="64" />
  <h1>togo-framework/auth-platform</h1>
  <p>
    <a href="https://to-go.dev/marketplace"><img src="https://img.shields.io/badge/marketplace-to--go.dev-1FC7DC" alt="marketplace" /></a>
    <a href="https://pkg.go.dev/github.com/togo-framework/auth-platform"><img src="https://pkg.go.dev/badge/github.com/togo-framework/auth-platform.svg" alt="pkg.go.dev" /></a>
    <img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT" />
  </p>
  <p><strong>Organizations & teams for <a href="https://to-go.dev">togo</a> — multi-tenant auth with per-org roles, invites & branding.</strong></p>
</div>

## Install

```bash
togo install togo-framework/auth-platform
```

`auth-platform` adds the **organization / team** layer on top of the togo `auth` plugin — what Fort calls *platforms* and Laravel Jetstream calls *teams*. Users join **orgs** as **members** with a per-org **role**, are added by **email invite**, and every request is scoped to a **current org** (resolved from a header, subdomain, or claim). Each org carries its own **settings** and **branding**. It composes with `auth` but works standalone.

## Usage

```go
import authplatform "github.com/togo-framework/auth-platform"

s, _ := authplatform.FromKernel(k)

// Create an org (the creator becomes the owner).
org, _ := s.CreateOrg("Acme Inc", "", ownerID)

// Invite by email, accept by token.
inv, _ := s.Invite(org.ID, "jane@acme.com", authplatform.RoleAdmin)
s.Accept(inv.Token, janeUserID)

// Roles & gating.
s.HasRole(org.ID, janeUserID, authplatform.RoleAdmin) // true
s.SetRole(org.ID, janeUserID, authplatform.RoleMember)

// Org switcher + per-org settings/branding.
orgs := s.OrgsForUser(userID)
s.SetSetting(org.ID, "feature.beta", true)
s.SetBranding(org.ID, authplatform.Branding{PrimaryColor: "#2C7BE2", LogoURL: "/logo.svg"})
```

### Request scoping

```go
// Resolve the current org from X-Org-Id / ?org= / subdomain, then read it anywhere.
router.Use(s.ResolveOrg)
orgID := authplatform.OrgID(ctx)
org, _ := s.CurrentOrg(ctx)

// Gate a route by org role (403 otherwise).
router.With(s.RequireOrgRole(authplatform.RoleAdmin)).Post("/api/billing", handler)
```

## Roles

`owner` > `admin` > `member` (ranked — `RequireOrgRole(admin)` is satisfied by owners). Custom role strings are allowed and matched by exact name.

## REST API

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/orgs` | orgs the current user belongs to (switcher) |
| `POST` | `/api/orgs` | create an org (creator = owner) |
| `GET/PATCH/DELETE` | `/api/orgs/{id}` | read / update branding+settings / delete |
| `GET` | `/api/orgs/{id}/members` | list members |
| `POST` | `/api/orgs/{id}/invites` | invite by email + role |
| `POST` | `/api/org-invites/accept` | accept an invite token |

The current user is read from the `auth` context (or `X-User-Id` for standalone use).

## Configuration

No required env. Data is held in a bounded in-memory store behind a small interface — back it with a database for persistence in production.

---

<div align="center">
  <h3>Premium sponsors</h3>
  <p>
    <a href="https://id8media.com"><strong>ID8 Media</strong></a> &nbsp;·&nbsp;
    <a href="https://one-studio.co"><strong>One Studio</strong></a>
  </p>
  <p><sub>Support togo — <a href="https://github.com/sponsors/fadymondy">become a sponsor</a>.</sub></p>
</div>
