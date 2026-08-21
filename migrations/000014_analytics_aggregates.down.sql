-- Dropping a continuous aggregate also removes its refresh policy, so the
-- policies added in the up migration need no separate teardown.
DROP MATERIALIZED VIEW IF EXISTS analytics_contract_activity_hourly CASCADE;
DROP MATERIALIZED VIEW IF EXISTS analytics_asset_transfers_hourly CASCADE;
DROP MATERIALIZED VIEW IF EXISTS analytics_asset_supply_hourly CASCADE;
DROP MATERIALIZED VIEW IF EXISTS analytics_active_accounts_weekly CASCADE;
DROP MATERIALIZED VIEW IF EXISTS analytics_active_accounts_daily CASCADE;
DROP MATERIALIZED VIEW IF EXISTS analytics_active_accounts_hourly CASCADE;
DROP MATERIALIZED VIEW IF EXISTS analytics_new_accounts_hourly CASCADE;
DROP MATERIALIZED VIEW IF EXISTS analytics_volume_hourly CASCADE;
DROP MATERIALIZED VIEW IF EXISTS analytics_tx_hourly CASCADE;

