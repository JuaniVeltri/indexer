# Soroban Domains read API

Frozen contract for reverse lookup and domain browsing. Explorer and TUI
clients should integrate against this shape. Availability is always signaled
in the JSON body (`indexed`), never by HTTP error status.

All endpoints return `200` on success, including empty results and the
not-yet-indexed state. `4xx`/`5xx` are reserved for malformed requests and
server failures.

Base path is the indexer HTTP server (`HTTP_ADDR`, or `METRICS_ADDR` if
`HTTP_ADDR` is unset).

## Envelope

```json
{
  "indexed": true,
  "domain": null,
  "domains": [],
  "events": [],
  "cursor": ""
}
```

| Field | Meaning |
| --- | --- |
| `indexed` | `false` until at least one ledger has been ingested. Clients must render a "Domain data isn't available yet" state and must not treat this as an error. |
| `domain` | Single record for resolve-by-name and the primary match for reverse lookup. `null` when missing. |
| `domains` | Present on list and reverse-lookup responses. |
| `events` | Present on per-domain history responses. |
| `cursor` | Last `name` on the current list page; pass as `cursor` to fetch the next page. |

## Domain record

```json
{
  "name": "alice.xlm",
  "owner": "G...",
  "address": "G...",
  "target_type": "account",
  "registered_at": "2026-01-01T00:00:00Z",
  "expires_at": "2027-01-01T00:00:00Z",
  "status": "active",
  "last_event_ledger": 12345
}
```

`target_type` is `account` (`G…`) or `contract` (`C…`).

`status` is computed at read time:

- `revoked` — the registry evicted the name
- `expired` — `expires_at` is in the past and the name was not revoked
- `active` — otherwise

Expired names are omitted from `status=active` lists but remain visible via
resolve-by-name and history.

## Endpoints

### Resolve by name

`GET /v1/domains/{name}`

`{name}` is the fully-qualified name (`alice.xlm`).

### Reverse lookup

`GET /v1/domains?address={G_or_C}`

Returns domains whose **current resolved address** is `address`. By default
only `active` records are included. Pass `status=all` to include expired and
revoked rows. `domain` is the first active match when any exist.

### List / paginate

`GET /v1/domains?status=active&limit=50&cursor=`

`status` is `active`, `expired`, `revoked`, or omitted (all named rows).
`limit` defaults to 50 (max 200). `cursor` is the last `name` from the
previous page.

### Event history

`GET /v1/domains/{name}/events?limit=50`

Events are ordered by ledger ascending. `event_type` is one of `register`,
`transfer`, `renew`, `claim`, `revoke`.
