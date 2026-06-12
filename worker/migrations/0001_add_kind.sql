-- Migration 0001: add the `kind` column to an ALREADY-DEPLOYED queue table.
--
-- Fresh deploys pick `kind` up from schema.sql (CREATE TABLE) and do NOT need
-- this file. It exists only for databases that were created before `kind`
-- shipped — D1's CREATE TABLE IF NOT EXISTS won't add a column to an existing
-- table, so the column must be added with ALTER TABLE.
--
-- Apply to the remote (deployed) database with:
--   wrangler d1 execute qi-queue --file=./migrations/0001_add_kind.sql --remote
--
-- Safe to run once. Re-running errors with "duplicate column name: kind"; that
-- error is harmless and means the migration already applied.

ALTER TABLE queue ADD COLUMN kind TEXT NOT NULL DEFAULT 'task';
