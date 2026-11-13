# Per-user WhatsApp implementation

Each messwa user has a separate whatsmeow sqlstore and private application database. The migrated admin keeps the original paths; new users use state/user-ID/. Store methods route only market tables to the shared board database. Existing payment claims, negotiation journals and task IDs are isolated without owner filters in every SQL statement.

The runtime selects private MCP servers and dashboard handlers from authenticated user IDs. Clients reconnect independently and WhatsApp logout no longer exits the process. Account-page QR linking, status polling and unlinking use existing session, CSRF and origin checks. CLI commands select the same private account. The service can start without linked devices or configured groups.

Linking or enabling sell tools sends no chat messages. Explicitly starting a task or arming delivery authorizes the existing sale flow. Unlink cancels active tasks before logging out. Runtime sends also check that the owner is still enabled.

## Migration

Opening an existing application database pins its first enabled admin in a persistent device_ownership row and assigns existing sell-task ownership in the same transaction. The old session and private data stay at existing paths, so no relink is required. Without an admin, assignment waits. Role changes and deletion do not transfer the original data or session. A permanent account-ID history prevents deleted IDs being reused.

Only group metadata, listings, posts, reactions, group logs, market prices and listing facts are shared. DMs, deliveries, payment IDs, offers, tasks, buyers, negotiations, journals, settings and WhatsApp contact/session stores are private. Group/message uniqueness deduplicates messages across clients. Every client's allowlist is checked before application storage, including CLI pairing history.

## Roles and settings

All users can link their own device, read their own DMs, see their own sales and operate their own enabled sell tasks. Insights remains admin-only; account management remains shell-only. README.md contains the final role table.

The account page edits selling settings, sell-tool enablement, unsaved-chat retention and the monthly bill fallback. The migrated admin retains file defaults and saved settings; new users start with sell tools and image payments disabled. Phone identity comes from each device. No configured UPI address existed to migrate.

## Limitations and verification

Dashboard pairing uses QR. Pairing codes remain available through login --user NAME --phone NUMBER. Live pairing, reconnect recovery and payments were not tested against WhatsApp. Tests use local databases and HTTP clients. Production was not accessed.

Deleted users' files remain on disk for operator-managed retention, with application access revoked. CLI commands opening the same WhatsApp session require the service to be stopped. The standalone dashboard supports scoped reads and settings; live linking and sale actions require run. No commits, pushes, PRs or deployment changes were made.

Final required checks:

```text
go build ./...: exit 0, no output
go vet ./...: exit 0, no output
go test ./...: exit 0
ok  messwa                         (cached)
ok  messwa/internal/accounts       (cached)
ok  messwa/internal/classify       (cached)
ok  messwa/internal/config         (cached)
ok  messwa/internal/dashboard      (cached)
ok  messwa/internal/ingest         (cached)
ok  messwa/internal/logfile        (cached)
ok  messwa/internal/market         (cached)
ok  messwa/internal/mcpserver      (cached)
ok  messwa/internal/meals          (cached)
ok  messwa/internal/portal         (cached)
ok  messwa/internal/repost         (cached)
ok  messwa/internal/store          (cached)
ok  messwa/internal/wa             (cached)
```

Also passed: git diff --check; JavaScript syntax checks; race checks for store, ingest, MCP and dashboard. Race checks preceded final migration and UI refinements; the required build, vet and full test suite were rerun afterward.

New tests cover private DMs and deletion, buyers, independent payment claims with identical IDs, sales, cross-account task-cancellation attempts over MCP, dashboard session routing and CSRF, settings restrictions, live-setting isolation, legacy session preservation, ownership migration, deleted-ID reuse prevention, board deduplication and rejection of disallowed groups.

## Changed files

- README.md
- cli_account.go
- config.example.toml
- delivery.go
- device_accounts.go
- device_accounts_test.go
- handoff.go
- inspect.go
- internal/config/config.go
- internal/config/settings.go
- internal/dashboard/dashboard.go
- internal/dashboard/device.go
- internal/dashboard/device_test.go
- internal/dashboard/sales.go
- internal/dashboard/sell_actions.go
- internal/dashboard/static/account.css
- internal/dashboard/static/account.html
- internal/dashboard/static/account.js
- internal/dashboard/static/app.js
- internal/ingest/multi_account_test.go
- internal/mcpserver/account.go
- internal/mcpserver/account_test.go
- internal/mcpserver/http.go
- internal/mcpserver/sell.go
- internal/mcpserver/sell_actions.go
- internal/mcpserver/server.go
- internal/store/accounts.go
- internal/store/facts.go
- internal/store/log.go
- internal/store/market.go
- internal/store/ownership.go
- internal/store/ownership_test.go
- internal/store/prices.go
- internal/store/sell_events.go
- internal/store/store.go
- internal/wa/wa.go
- main.go
- sold.go
- users.go
- IMPLEMENTATION.md (this report)

## Unlink lifecycle correction

Unlink now retires the complete whatsmeow client and creates a new client with a fresh device in the same user's sqlstore container. A stable, synchronized per-account runtime is shared by the account manager, dashboard pairing/status, MCP contacts, sale connection adapter, send closure, connection waiter and own-message identity lookup. Calls lease the current generation; replacement drains existing calls, makes the retiring generation unavailable, cancels its context, disconnects it, removes/drains handlers and joins ingest refresh/reconnect workers before publishing its replacement. Event registrations keep stable IDs through one dispatcher on each generation; ingest and its allowlist are installed once on the new client. The container remains open for the replacement and closes on account shutdown.

Regression tests cover repeated unlink and QR-channel setup without a live WhatsApp connection, distinct client/device instances, old-handler removal, exactly-once registration and callback dispatch, worker draining, concurrent operations, stable production consumer wiring and another user's unaffected client. Explicit task/delivery send authorization and deleted-account file retention are unchanged. Live WhatsApp pairing/logout remains untested.

Verification after this correction: `go build ./...`, `go vet ./...`, and `go test ./... -timeout 15m` passed. `go test -race ./... -timeout 20m` also passed for every package (root: 145.693s; wa: 2.783s). `git diff --check` passed. Identity is read from a synchronized event-derived snapshot, and handler dispatch preserves registration order.
