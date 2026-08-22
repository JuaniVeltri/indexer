-- Network-wide analytics aggregates backing /api/v1/analytics/*.
--
-- Metrics are materialized hourly. Daily and weekly series are re-bucketed from
-- these hourly aggregates at read time, which keeps the schema small: a 30-day
-- daily series reads 720 rows. active_accounts is the exception — distinct
-- counts are not additive, so summing 24 hourly distinct counts would overstate
-- a day. It gets one aggregate per resolution, each computed from the raw table.
--
-- Every aggregate sets materialized_only = false. The default is TRUE since
-- TimescaleDB 2.13, which would make each series stop at the refresh watermark
-- rather than unioning in raw data written since the last refresh. With it off,
-- queries stay fresh and the trailing bucket is partial (documented in
-- docs/analytics-api.md).
-- https://www.tigerdata.com/docs/api/latest/continuous-aggregates/create_materialized_view
--
-- DISTINCT and FILTER inside aggregate functions require TimescaleDB 2.7+.
-- https://www.tigerdata.com/docs/use-timescale/latest/continuous-aggregates/about-continuous-aggregates
--
-- All views are created WITH NO DATA so this migration applies instantly to a
-- database that already holds history; `indexer analytics-backfill` populates
-- them afterwards.

-- Transaction counts and fee totals.
--
-- fee_soroban is the TOTAL fee charged on Soroban transactions, not the Soroban
-- resource fee. The split is by is_soroban because transactions.soroban_resources
-- is never populated by the transform layer, so the resource component cannot be
-- isolated — reading this column as a resource fee overstates it by the inclusion
-- fee on every Soroban transaction. Resource fees are tracked separately in #43.
CREATE MATERIALIZED VIEW analytics_tx_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    time_bucket(INTERVAL '1 hour', created_at)     AS bucket,
    COUNT(*)                                       AS tx_count,
    SUM(fee_charged) FILTER (WHERE NOT is_soroban) AS fee_classic,
    SUM(fee_charged) FILTER (WHERE is_soroban)     AS fee_soroban
FROM transactions
GROUP BY bucket
WITH NO DATA;

-- Native value moved, in stroops. Only `transfer` events count: CAP-67 emits a
-- `fee` event for every transaction, and including those would inflate volume
-- by more than an order of magnitude. amount is used rather than
-- amount_formatted because the transform layer never populates the latter.
CREATE MATERIALIZED VIEW analytics_volume_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    time_bucket(INTERVAL '1 hour', created_at) AS bucket,
    SUM(amount)                                AS stroops_transferred,
    COUNT(*)                                   AS transfer_count
FROM token_events
-- event_type rather than event_type_name: the index and the compression
-- segment-by are on the numeric column, so filtering on the text one scans and
-- decompresses instead of pruning. 0 = transfer.
WHERE event_type = 0
  AND asset_type = 0
GROUP BY bucket
WITH NO DATA;

-- Accounts created on the network.
CREATE MATERIALIZED VIEW analytics_new_accounts_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    time_bucket(INTERVAL '1 hour', created_at) AS bucket,
    COUNT(*)                                   AS new_accounts
FROM operations
-- type rather than type_name, to use idx_op_type and the compression
-- segment-by. 0 = create_account.
WHERE type = 0
GROUP BY bucket
WITH NO DATA;

-- Distinct transaction source accounts. One aggregate per resolution, because a
-- distinct count cannot be rolled up from a finer bucket.
CREATE MATERIALIZED VIEW analytics_active_accounts_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    time_bucket(INTERVAL '1 hour', created_at) AS bucket,
    COUNT(DISTINCT account)                    AS active_accounts
FROM transactions
GROUP BY bucket
WITH NO DATA;

CREATE MATERIALIZED VIEW analytics_active_accounts_daily
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    time_bucket(INTERVAL '1 day', created_at) AS bucket,
    COUNT(DISTINCT account)                   AS active_accounts
FROM transactions
GROUP BY bucket
WITH NO DATA;

CREATE MATERIALIZED VIEW analytics_active_accounts_weekly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    time_bucket(INTERVAL '1 week', created_at) AS bucket,
    COUNT(DISTINCT account)                    AS active_accounts
FROM transactions
GROUP BY bucket
WITH NO DATA;

-- Signed supply change per asset. Mints add, burns and clawbacks remove. Kept
-- per asset so a single-asset series can be served later without re-aggregating.
CREATE MATERIALIZED VIEW analytics_asset_supply_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    time_bucket(INTERVAL '1 hour', created_at) AS bucket,
    asset_type,
    asset_code,
    asset_issuer,
    asset_contract_id,
    SUM(CASE WHEN event_type = 1 THEN amount ELSE -amount END) AS net_supply_delta
FROM token_events
-- 1 = mint, 2 = burn, 3 = clawback.
WHERE event_type IN (1, 2, 3)
GROUP BY bucket, asset_type, asset_code, asset_issuer, asset_contract_id
WITH NO DATA;

-- Transferred volume per asset, for the asset_transfers ranking. Amounts are in
-- each asset's base units; the read layer scales them by the asset's decimals.
CREATE MATERIALIZED VIEW analytics_asset_transfers_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    time_bucket(INTERVAL '1 hour', created_at) AS bucket,
    asset_type,
    asset_code,
    asset_issuer,
    asset_contract_id,
    SUM(amount)                                AS amount_transferred,
    COUNT(*)                                   AS transfer_count
FROM token_events
WHERE event_type = 0
GROUP BY bucket, asset_type, asset_code, asset_issuer, asset_contract_id
WITH NO DATA;

-- Contract activity, measured as events emitted per contract. operations
-- carries no contract_id — the transform layer records only the host function
-- type for invoke_host_function — so contract_events is the only per-contract
-- signal currently available.
CREATE MATERIALIZED VIEW analytics_contract_activity_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    time_bucket(INTERVAL '1 hour', created_at) AS bucket,
    contract_id,
    COUNT(*)                                   AS event_count
FROM contract_events
GROUP BY bucket, contract_id
WITH NO DATA;

-- Refresh policies. start_offset must be greater than end_offset, and
-- end_offset is kept at one bucket width so a policy never competes with the
-- writes landing in the current bucket; real-time aggregation covers that gap.
-- https://www.tigerdata.com/docs/api/latest/continuous-aggregates/add_continuous_aggregate_policy
SELECT add_continuous_aggregate_policy('analytics_tx_hourly',
    start_offset => INTERVAL '30 days', end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes');

SELECT add_continuous_aggregate_policy('analytics_volume_hourly',
    start_offset => INTERVAL '30 days', end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes');

SELECT add_continuous_aggregate_policy('analytics_new_accounts_hourly',
    start_offset => INTERVAL '30 days', end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes');

SELECT add_continuous_aggregate_policy('analytics_active_accounts_hourly',
    start_offset => INTERVAL '30 days', end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes');

SELECT add_continuous_aggregate_policy('analytics_active_accounts_daily',
    start_offset => INTERVAL '90 days', end_offset => INTERVAL '1 day',
    schedule_interval => INTERVAL '1 hour');

SELECT add_continuous_aggregate_policy('analytics_active_accounts_weekly',
    start_offset => INTERVAL '1 year', end_offset => INTERVAL '1 week',
    schedule_interval => INTERVAL '6 hours');

SELECT add_continuous_aggregate_policy('analytics_asset_supply_hourly',
    start_offset => INTERVAL '30 days', end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes');

SELECT add_continuous_aggregate_policy('analytics_asset_transfers_hourly',
    start_offset => INTERVAL '30 days', end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes');

SELECT add_continuous_aggregate_policy('analytics_contract_activity_hourly',
    start_offset => INTERVAL '30 days', end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes');
