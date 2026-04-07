# Admin API User Lifecycle And Runtime Control Design

Date: 2026-04-07
Status: Proposed

## Goal

Extend `admin-api` from a narrow runtime quota/speed endpoint into a unified control plane for:

- administrator authentication
- user create / read / update / delete
- runtime traffic-quota updates
- runtime speed-limiter updates
- runtime per-user conditional speed schedules

The design keeps ownership boundaries explicit:

- `user-provider` owns user lifecycle and pushes authenticated users into managed inbounds
- `traffic-quota` owns runtime traffic-limit state
- `speed-limiter` owns runtime speed state and per-user conditional schedules
- `admin-api` owns HTTP, authentication, request validation, and orchestration only

## Problem Statement

The current `admin-api` can update per-user quota and fixed speed, but it cannot:

- authenticate administrators with either bearer tokens or admin username/password
- create, update, or delete users
- expose a single control-plane API for user lifecycle plus runtime controls
- manage per-user time-based speed rules at runtime

This leaves runtime operations split across different mechanisms and prevents `admin-api` from serving as the single operator-facing surface.

## Scope

### In Scope

- add admin authentication to `admin-api`
- support static bearer tokens
- support admin username/password login that issues a short-lived token
- add `POST`-only admin endpoints for user, quota, speed, and schedule operations
- add runtime write APIs to `user-provider`
- add runtime per-user schedule APIs to `speed-limiter`
- keep `traffic-quota` runtime APIs as the quota control surface
- aggregate user + quota + speed + schedule state in admin read endpoints

### Out Of Scope

- group lifecycle management
- persistent storage for admin-api overlay state across process restart
- refresh tokens
- external IAM / OAuth / RBAC
- per-endpoint authorization scopes
- migration of all existing external data sources to writable mode

## Requirements

- administrators can authenticate with either:
- `Authorization: Bearer <token>`
- `Authorization: Basic <base64(username:password)>`
- administrators can log in with username/password and receive a short-lived token
- all business endpoints use `POST`
- admin API can create, update, get, list, and delete users
- admin API can immediately update runtime quota state
- admin API can immediately update runtime speed state
- admin API can immediately update runtime per-user speed schedules
- deleting a user removes new-auth capability first, then clears runtime limit state

## Control-Plane Model

### Service Responsibilities

#### `admin-api`

- validates admin credentials
- issues stateless login tokens
- decodes request JSON
- resolves the managed services from `ServiceManager`
- orchestrates calls across `user-provider`, `traffic-quota`, and `speed-limiter`
- returns operator-facing responses and error codes

#### `user-provider`

- remains the authority for runtime user lifecycle
- stores admin-created or admin-updated users in an in-memory overlay layer
- merges the overlay with existing inline/file/http/redis/postgres sources
- pushes the final merged user set to all managed inbounds

#### `traffic-quota`

- remains the authority for runtime quota state
- owns usage, pending delta, exceeded state, connection close behavior, and quota removal

#### `speed-limiter`

- remains the authority for runtime speed state
- owns fixed per-user speed overrides
- owns new runtime per-user schedules
- computes effective speed with fixed precedence rules

## Authentication Design

### Admin API Config

Add fields to `option.AdminAPIServiceOptions`:

- `token_secret string`
- `token_ttl badoption.Duration`
- `tokens []string`
- `admins []AdminCredential`

Add a new credential type:

- `username string`
- `password string`

### Accepted Auth Methods

#### Static bearer token

If request header contains `Authorization: Bearer <token>`, the token is accepted when:

- it matches one of the configured static tokens, or
- it is a valid signed login token produced by `POST /admin/v1/auth/login`

#### Basic auth

If request header contains `Authorization: Basic ...`, the decoded username/password must match one configured admin credential.

### Login Token

`POST /admin/v1/auth/login` accepts:

```json
{
  "username": "admin",
  "password": "secret"
}
```

Returns:

```json
{
  "token": "signed-token",
  "expires_at": "2026-04-08T12:00:00Z"
}
```

Token properties:

- stateless
- signed with `token_secret`
- carries `sub=username`
- carries `exp`
- default TTL 12h if unspecified

No refresh token support is added in this design.

## HTTP API Design

All endpoints use `POST`.

### Authentication

- `POST /admin/v1/auth/login`

### User Lifecycle

- `POST /admin/v1/user/list`
- `POST /admin/v1/user/get`
- `POST /admin/v1/user/create`
- `POST /admin/v1/user/update`
- `POST /admin/v1/user/delete`

### Quota

- `POST /admin/v1/quota/get`
- `POST /admin/v1/quota/update`
- `POST /admin/v1/quota/delete`

### Speed

- `POST /admin/v1/speed/get`
- `POST /admin/v1/speed/update`
- `POST /admin/v1/speed/delete`

### User Speed Schedules

- `POST /admin/v1/speed/schedule/get`
- `POST /admin/v1/speed/schedule/update`
- `POST /admin/v1/speed/schedule/delete`

## Request/Response Shapes

### User Create

Request:

```json
{
  "user": {
    "name": "alice",
    "password": "alice-pass",
    "uuid": "",
    "alter_id": 0,
    "flow": ""
  },
  "quota": {
    "quota_gb": 0.04,
    "period": "daily",
    "period_start": "",
    "period_days": 0
  },
  "speed": {
    "upload_mbps": 5,
    "download_mbps": 10
  },
  "speed_schedules": [
    {
      "time_range": "08:00-18:00",
      "upload_mbps": 2,
      "download_mbps": 5
    }
  ]
}
```

Behavior:

- create user in `user-provider` overlay
- if `quota` exists, apply runtime quota
- if `speed` exists, apply runtime fixed speed
- if `speed_schedules` exists, replace runtime user schedules

### User Update

Request:

```json
{
  "user": "alice",
  "patch": {
    "password": "new-pass",
    "uuid": "",
    "alter_id": 0,
    "flow": ""
  },
  "quota": {
    "quota_gb": 0.08,
    "period": "monthly"
  },
  "speed": {
    "upload_mbps": 20
  },
  "speed_schedules": [
    {
      "time_range": "18:00-23:00",
      "upload_mbps": 10,
      "download_mbps": 20
    }
  ]
}
```

Behavior:

- partial update of user identity/auth fields
- optional quota update
- optional fixed speed update
- optional full schedule replacement

### User Get

Request:

```json
{
  "user": "alice"
}
```

Response is an aggregate view:

```json
{
  "user": {
    "name": "alice",
    "password": "",
    "uuid": "",
    "alter_id": 0,
    "flow": ""
  },
  "quota": {
    "usage_bytes": 900,
    "quota_bytes": 1024,
    "exceeded": false
  },
  "speed": {
    "upload_mbps": 5,
    "download_mbps": 10
  },
  "speed_schedules": [
    {
      "time_range": "08:00-18:00",
      "upload_mbps": 2,
      "download_mbps": 5
    }
  ]
}
```

### User Delete

Request:

```json
{
  "user": "alice",
  "purge_limits": true
}
```

Delete sequence:

1. remove user from `user-provider` overlay and push to inbounds
2. remove quota runtime state
3. remove fixed speed runtime state
4. remove speed schedules runtime state

This guarantees new connections stop before runtime state cleanup finishes.

## `user-provider` Runtime Write Design

### New Internal State

Add an admin overlay map to `user-provider.Service`:

- `overlayUsers map[string]option.User`

The overlay holds all admin-created or admin-updated users.

### Merge Rule

`loadAndPush()` becomes:

1. gather inline/file/http/redis/postgres users
2. normalize into a map keyed by username
3. apply `overlayUsers` last
4. convert final map to ordered user list
5. push to managed inbounds

This gives admin writes highest runtime precedence without mutating external sources.

### New Methods

- `ListUsers() []adapter.User`
- `GetUser(name string) (adapter.User, bool)`
- `CreateUser(user option.User) error`
- `UpdateUser(name string, patch UserPatch) error`
- `DeleteUser(name string) error`

`UserPatch` only mutates fields explicitly provided by the admin request.

## `speed-limiter` Runtime Schedule Design

### New User Schedule State

Add runtime user schedule storage:

- `userSchedules map[string][]UserSchedule`

`UserSchedule` mirrors the existing schedule shape:

- `time_range`
- `upload_mbps`
- `download_mbps`

No group field is needed for user-level schedules.

### New Methods

- `GetUserSchedules(user string) ([]UserSchedule, bool)`
- `ReplaceUserSchedules(user string, schedules []UserSchedule) error`
- `RemoveUserSchedules(user string) error`

### Effective Speed Precedence

The effective runtime speed for a user is resolved in this order:

1. user fixed speed override
2. user schedule if current time matches
3. group schedule if current time matches
4. group fixed speed
5. default fixed speed

This precedence is mandatory and stable across API, runtime application, and tests.

## `traffic-quota` Runtime Control Design

No structural ownership change is needed.

`admin-api` will continue to call:

- `ApplyConfig`
- `RemoveConfig`
- `QuotaStatus`

For user create/update requests that include quota payload, quota is applied immediately after the user lifecycle operation succeeds.

## Admin API Orchestration Rules

### User Create

1. authenticate admin
2. validate request
3. create user via `user-provider`
4. apply quota if present
5. apply fixed speed if present
6. replace user schedules if present
7. return success

### User Update

1. authenticate admin
2. validate request
3. update user via `user-provider`
4. update quota if present
5. update fixed speed if present
6. replace schedules if present
7. return aggregated result or no-content

### User Delete

1. authenticate admin
2. delete user via `user-provider`
3. remove quota runtime state
4. remove fixed speed runtime state
5. remove speed schedule runtime state
6. return success

## Error Handling

- unauthenticated: `401`
- malformed request JSON: `400`
- invalid business values: `400`
- duplicate user on create: `409`
- missing user on get/update/delete: `404`
- required managed service missing: `503`
- internal orchestration failure: `500`

Partial write semantics:

- user create/update/delete is orchestrated step-by-step
- if user lifecycle succeeds but a limit step fails, return failure and include which step failed
- no cross-service transactional rollback is introduced in this design
- failures must be explicit in logs and API response body

## Testing Matrix

### Authentication

- bearer token success
- static token success
- basic auth success
- expired login token rejected
- invalid password rejected
- no auth rejected

### User Lifecycle

- create user and verify user-provider pushes to inbounds
- update user password and verify new connections require the new credential
- get existing user returns aggregate state
- list returns merged source + overlay users
- delete user removes it from managed inbounds

### Runtime Quota

- create user with quota applies immediately
- raise quota clears exceeded state when usage is below new limit
- lower quota trips exceeded state when usage exceeds new limit
- delete user removes quota state

### Runtime Fixed Speed

- create user with fixed speed applies immediately
- update fixed speed changes active limiter state
- delete user removes fixed speed override

### Runtime User Schedules

- replace user schedules applies the new set atomically
- active time window updates effective limiter rate
- deleting schedules restores group/default behavior
- fixed user speed remains higher precedence than user schedules

### Orchestration

- user create with user + quota + speed + schedules
- user update with only auth patch
- user update with only quota patch
- user delete removes lifecycle first, then runtime state
- missing `user-provider` / `traffic-quota` / `speed-limiter` yields `503`

## Operational Notes

- admin overlay users are runtime-only in this design; they are not persisted to postgres/redis/file/http sources
- restart clears admin overlay users unless a later design adds persistence
- login tokens are stateless and invalidated by expiry only
- administrators can still use Basic auth directly without calling login

## Risks

- admin overlay precedence can temporarily diverge from external data sources after restart
- lack of rollback means multi-step create/update can leave partial success across services
- per-user runtime schedule support increases `speed-limiter` state complexity and test surface

## Recommendation

Implement in phases:

1. admin authentication and `POST` user lifecycle endpoints
2. `user-provider` runtime overlay write API
3. user aggregate read endpoints
4. user-level speed schedule runtime support
5. full orchestration tests
