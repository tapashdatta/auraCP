-- Multi-table SELECT JOIN.
SELECT u.id, o.id FROM users u JOIN orders o ON o.uid = u.id;
-- Schema-qualified.
SELECT * FROM mydb.users;
-- Subquery — should record bans.
UPDATE users SET x=1 WHERE id IN (SELECT uid FROM bans);
-- INSERT ... SELECT.
INSERT INTO logs (ts, msg) SELECT NOW(), msg FROM staging.events;
-- CREATE TABLE AS SELECT — both target and source recorded.
CREATE TABLE archive AS SELECT * FROM users;
-- CTE — the binding name 'x' is NOT a table; source IS.
WITH x AS (SELECT * FROM source) SELECT * FROM x;
