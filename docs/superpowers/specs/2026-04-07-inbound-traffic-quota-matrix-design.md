# Inbound User Traffic Quota Matrix Design

Date: 2026-04-07
Status: Drafted and self-reviewed

## Summary

Add a protocol-matrix test suite for the `traffic-quota` service to verify that per-user total traffic accounting works across supported inbound protocols that populate `metadata.User`.

The suite will validate three behaviors for each covered inbound protocol:

- traffic stays available while the user remains under quota
- the active connection is cut once cumulative traffic exceeds quota
- new connections are rejected immediately after quota exhaustion

The goal is protocol coverage, not persistence coverage. These tests will run in memory mode and focus on end-to-end enforcement through the `ConnectionTracker` path.

## Problem

`traffic-quota` has initial end-to-end coverage, but only for a narrow protocol slice. The service is intended to work transparently for all user-aware inbound protocols because enforcement is attached at the router tracker layer, not in protocol-specific code.

Without a matrix test suite, regressions can hide in any of the following areas:

- a protocol stops setting `metadata.User`
- a protocol still authenticates but no longer integrates correctly with `user-provider`
- tracker wrapping works for one protocol family but not another
- transport-specific behavior causes cutoff or reconnection logic to diverge

## Goal

Build a reusable end-to-end test structure that verifies user total traffic quota enforcement for supported inbound protocols.

Success means:

- each covered inbound has its own explicit test entrypoint
- all tests share the same quota assertions and setup conventions
- failures identify the broken protocol directly
- the suite remains fast enough for routine targeted execution

## Scope

### In scope

- inbound protocols that both support dynamic users and assign `metadata.User`
- shared helper infrastructure for quota test setup
- protocol-specific tests for under-quota success and over-quota rejection
- one or a small number of representative 10 MB tests for realistic volume validation

### Out of scope

- protocols without a user identity concept
- persistence backend validation for Redis or Postgres
- real-time period-expiry end-to-end testing across all protocols
- refactoring protocol auth code
- adding API assertions or usage-query APIs

## Covered Protocols

The matrix should cover the inbound protocols currently confirmed to support user identity through `metadata.User`:

- `socks`
- `http`
- `mixed`
- `shadowsocks` multi-user mode
- `trojan`
- `vless`
- `vmess`
- `naive`
- `hysteria`
- `hysteria2`
- `tuic`
- `shadowtls`
- `anytls`

Protocols without user identity semantics are excluded from this matrix and should not be forced into quota tests.

## Test Strategy

### Coverage model

Each protocol test will verify:

1. the authenticated user can transfer data while under quota
2. cumulative traffic beyond quota causes the active connection to terminate
3. after exhaustion, a newly established connection for the same user fails immediately

This suite intentionally does not include real one-day expiry transitions. Period reset behavior is already a better fit for service or manager tests, where time can be controlled directly without making the e2e matrix unstable.

### Data-size policy

The matrix will use two quota tiers:

- fast protocol sweep:
  - quota around `1 MB`
  - initial successful transfer around `256 KB`
  - overflow transfer large enough to exceed the quota by a clear margin
- representative realistic check:
  - one or a small number of dedicated tests with `10 MB` quota
  - target a stable protocol such as `trojan` or `vmess`

This keeps the suite fast while still validating the real-world volume assumption once in a representative path.

## Test Architecture

### Shared helper layer

Create a shared helper that owns the common setup:

- user-provider service configuration
- traffic-quota service configuration
- data server startup
- client dialer creation
- common assertions for under-quota success, over-quota cutoff, and post-exhaustion rejection

The helper must be protocol-agnostic and accept a protocol-specific fixture description rather than embedding protocol choices directly.

### Protocol fixture contract

Each protocol fixture should define:

- inbound type and options
- outbound type and options
- user fields required by that protocol
- whether TCP only or TCP and UDP are in scope for that protocol test
- certificate or transport requirements if needed

This keeps protocol variance in small per-protocol descriptors instead of growing the shared helper into a large switch statement.

### Test file layout

Do not keep all matrix tests in a single large file.

Preferred structure:

- `test/traffic_quota_helper_test.go`
- protocol-group test files, for example:
  - `test/traffic_quota_tcp_inbounds_test.go`
  - `test/traffic_quota_quic_inbounds_test.go`
  - or a similarly clear grouping by transport family

Each protocol still gets an explicit top-level test function, such as:

- `TestTrojanTrafficQuota`
- `TestVLESSTrafficQuota`
- `TestVMessTrafficQuota`
- `TestSOCKSTrafficQuota`

This preserves clear failure attribution in CI output.

## Protocol Rollout Order

Implementation should proceed in two waves.

### Wave 1

Start with the most stable and common user-aware protocols:

- `trojan`
- `vless`
- `vmess`
- `shadowsocks` multi-user mode
- `socks`
- `http`
- `mixed`

These protocols are sufficient to harden the helper design and validate the core matrix pattern.

### Wave 2

Extend the same structure to:

- `naive`
- `hysteria`
- `hysteria2`
- `tuic`
- `shadowtls`
- `anytls`

These have greater transport variation and should reuse the already-proven helper design from Wave 1.

## Assertions and Expected Behavior

### Under quota

The first transfer must complete successfully without premature cutoff.

### Over quota

The second transfer must terminate once cumulative bytes exceed the configured total quota. Because the service uses post-check accounting, a small overrun is acceptable; the assertion should verify cutoff after exhaustion, not exact byte-perfect termination at the threshold.

### Reconnect after exhaustion

A new connection for the same user must fail immediately or fail on first data operation. The assertion should tolerate protocol-specific surface differences in where that failure becomes visible, but it must still prove the exhausted user cannot continue using the service.

## Risks

- protocol-specific auth fields differ, so fixture descriptors must supply the correct user shape
- `shadowsocks` must explicitly use multi-user mode for meaningful user-identity quota testing
- QUIC-based protocols may be slower or more fragile, so data sizes must stay modest in the matrix sweep
- some protocols may need certificate or transport bootstrap that should remain inside the fixture, not leak into the common helper

## Verification Plan

Minimum expected verification after implementation:

- targeted `go test` runs for the new traffic-quota matrix tests
- successful execution of Wave 1 protocol tests
- successful execution of representative `10 MB` test
- successful execution of existing traffic-quota tests to confirm no regression of the current helper path

## Recommendation

Proceed with a two-wave protocol matrix:

- Wave 1 establishes the helper design on the most common protocols
- Wave 2 extends coverage to the more transport-specific protocols

This keeps the first implementation focused while still producing a design that scales to the full inbound protocol set.
