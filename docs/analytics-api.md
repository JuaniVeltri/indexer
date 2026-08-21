# Network Analytics API

Read-only HTTP API exposing network-wide time series and Top-N rankings, backed by TimescaleDB
continuous aggregates.

**This contract is frozen.** The explorer builds its dashboards against these shapes
(`stellarview-explorer/apps/explorer-web/src/lib/indexer/`). Field names, metric identifiers, and
the empty-result behaviour must not change without coordinating with that repository.

## Running it

```bash
API_ADDR=:8080 ./bin/indexer serve
```

`serve` starts only the read API — no ingestion — which is what the explorer's
`NEXT_PUBLIC_INDEXER_URL` points at. The same routes are also mounted on the `live` command's
metrics server when `METRICS_ADDR` is set, which is convenient in development.

## `GET /api/v1/analytics/timeseries`

| Parameter | Required | Values |
| ---------- | -------- | ------ |
| `metric` | yes | `tx_count`, `tx_volume`, `fee_classic`, `fee_soroban`, `active_accounts`, `new_accounts`, `asset_supply` |
| `resolution` | yes | `hourly`, `daily`, `weekly` |
| `from` | yes | RFC 3339 timestamp, inclusive |
| `to` | yes | RFC 3339 timestamp, exclusive |

```bash
curl 'localhost:8080/api/v1/analytics/timeseries?metric=tx_count&resolution=hourly&from=2026-08-20T19:00:00Z&to=2026-08-20T23:00:00Z'
```

```json
{
  "metric": "tx_count",
  "resolution": "hourly",
  "from": "2026-08-20T19:00:00Z",
  "to": "2026-08-20T23:00:00Z",
  "data": [
    { "timestamp": "2026-08-20T19:00:00Z", "value": 4821 },
    { "timestamp": "2026-08-20T20:00:00Z", "value": 5140 }
  ]
}
```

## `GET /api/v1/analytics/top`

| Parameter | Required | Values |
| ---------- | -------- | ------ |
| `metric` | yes | `contract_activity`, `asset_transfers`, `highest_fees` |
| `window` | yes | `24h`, `7d`, `30d` |
| `limit` | no | 1–100, default 10 |

```bash
curl 'localhost:8080/api/v1/analytics/top?metric=contract_activity&window=24h&limit=10'
```

```json
{
  "metric": "contract_activity",
  "window": "24h",
  "data": [
    {
      "id": "CAABC...",
      "label": "Soroswap Router",
      "value": 12043,
      "metadata": { "contract_type": 0 }
    }
  ]
}
```

`metadata` is omitted when a metric has no extra context to report.

## Metric definitions

| Metric | Source | Value | Unit |
| ------ | ------ | ----- | ---- |
| `tx_count` | `transactions` | `COUNT(*)` | transactions |
| `tx_volume` | `token_events` (`transfer`, native asset) | `SUM(amount_formatted)` | XLM |
| `fee_classic` | `transactions` where `NOT is_soroban` | `SUM(fee_charged)` | stroops |
| `fee_soroban` | `transactions` where `is_soroban` | `SUM(fee_charged)` | stroops |
| `active_accounts` | `transactions` | `COUNT(DISTINCT account)` | accounts |
| `new_accounts` | `operations` where `type_name = 'create_account'` | `COUNT(*)` | accounts |
| `asset_supply` | `token_events` (`mint`, `burn`, `clawback`) | net minted minus burned | asset units |

| Top-N metric | Source | `id` | `value` |
| ------------ | ------ | ---- | ------- |
| `contract_activity` | `operations` with a `contract_id` | contract ID | invocations |
| `asset_transfers` | `token_events` (`transfer`) | `CODE-ISSUER`, or `native` | transferred volume |
| `highest_fees` | `transactions` | transaction hash | fee charged, in stroops |

Notes on the definitions:

- **`tx_volume` counts transfers only.** CAP-67 emits a `fee` token event for every transaction;
  on a sample of testnet data those were 78k of 91k total events. Including them would inflate
  volume by more than an order of magnitude, so only `transfer` events on the native asset count.
- **`fee_soroban` is the total fee charged on Soroban transactions**, not the isolated resource-fee
  component. `transactions.soroban_resources` exists in the schema but the transform layer does not
  populate it yet, so the resource fee cannot be separated from the inclusion fee. Splitting them
  requires extracting `SorobanTransactionData.resourceFee` during transform — tracked separately.
- **`asset_supply` sums signed deltas across every asset** when queried through this endpoint.
  Because assets have different units, that total is an activity indicator rather than a monetary
  figure. The underlying aggregate is stored per asset, so a per-asset series can be exposed later
  without changing the stored data.

## Semantics

**Empty is not an error.** A metric with nothing aggregated yet returns `200` with `"data": []`.
The explorer uses exactly that to render its "not available yet" state, so these endpoints never
answer `404` for a valid-but-unpopulated metric. Only malformed parameters produce `400`, and only
a genuine backend failure produces `500`.

**Bucket boundaries follow `time_bucket`, in UTC.** Buckets of a day or more are measured from
2000-01-03, not the UNIX epoch, which puts weekly boundaries on a **Monday**. Alignment is identical
across resolutions and across metrics, which is what lets a daily series derived from hourly rows
agree with one computed directly from the raw tables.

**The most recent bucket is partial.** The aggregates are created with
`timescaledb.materialized_only = false`, so a query transparently unions materialized rows with
live raw data past the refresh watermark. Data stays fresh, and the trailing bucket covers only the
elapsed part of its interval. Clients rendering a "current hour" point should treat it as
in-progress.

**Top-N windows are closed at both ends.** A ranking covers `[now - window, now)`, so a row
timestamped ahead of the server clock cannot appear in a "last 24 hours" list.

**Ranking ties are stable.** Every Top-N query orders by value descending and then by a
deterministic secondary key — contract ID, asset key, or transaction hash — so repeating the same
query over the same window always returns the same list in the same order.

**Ranges are bounded.** A request may span at most 10,000 buckets at the requested resolution;
beyond that the API answers `400` rather than serialising an unbounded response. Every realistic
dashboard range is far below the limit — 10,000 buckets is 416 days of hourly data.

**Values are JSON numbers** (IEEE-754 doubles), matching the `number` type the explorer client
expects. Amounts far beyond 2^53 significant units would lose precision, which no current metric
approaches.

## Aggregation setup

Metrics are materialized hourly. Daily and weekly series are re-bucketed from the hourly
aggregate at query time, which keeps the schema small — a 30-day daily series reads 720 rows.

`active_accounts` is the exception. Distinct counts are not additive, so summing 24 hourly distinct
counts overstates a day's distinct accounts. It therefore has a dedicated aggregate per resolution,
each computed from the raw table.

| Aggregate | Grouped by | Feeds |
| --------- | ---------- | ----- |
| `analytics_tx_hourly` | bucket | `tx_count`, `fee_classic`, `fee_soroban` |
| `analytics_volume_hourly` | bucket | `tx_volume` |
| `analytics_new_accounts_hourly` | bucket | `new_accounts` |
| `analytics_asset_supply_hourly` | bucket, asset | `asset_supply` |
| `analytics_contract_activity_hourly` | bucket, contract | `contract_activity` |
| `analytics_asset_transfers_hourly` | bucket, asset | `asset_transfers` |
| `analytics_active_accounts_{hourly,daily,weekly}` | bucket | `active_accounts` |

`highest_fees` ranks individual transactions, which no aggregate can summarise, so it reads
`transactions` directly through an index on `(fee_charged DESC, created_at DESC)`.

Each aggregate carries a refresh policy that runs every 30 minutes over the trailing 30 days,
excluding the newest bucket so the policy never competes with active writes.

### Backfilling historical data

The migration creates the aggregates empty (`WITH NO DATA`), so it applies instantly to a database
that already holds history. Populate them once afterwards:

```bash
./bin/indexer analytics-backfill                                  # everything in the ledgers table
./bin/indexer analytics-backfill --from 2026-01-01T00:00:00Z      # from a point in time
```

The command refreshes each aggregate over the requested range. TimescaleDB processes the refresh in
batches, each in its own transaction, so an interrupted run can simply be re-run — buckets already
materialized are skipped.

## References

- [Continuous aggregates](https://www.tigerdata.com/docs/use-timescale/latest/continuous-aggregates/about-continuous-aggregates)
- [`CREATE MATERIALIZED VIEW`](https://www.tigerdata.com/docs/api/latest/continuous-aggregates/create_materialized_view)
- [`refresh_continuous_aggregate`](https://www.tigerdata.com/docs/api/latest/continuous-aggregates/refresh_continuous_aggregate)
- [`add_continuous_aggregate_policy`](https://www.tigerdata.com/docs/api/latest/continuous-aggregates/add_continuous_aggregate_policy)
