SELECT u.id, o.id FROM users u JOIN orders o ON o.uid = u.id;
SELECT * FROM pg_catalog.pg_tables;
UPDATE users SET x=1 WHERE id IN (SELECT uid FROM bans);
INSERT INTO logs (ts, msg) SELECT NOW(), msg FROM staging.events;
-- COPY forbidden but Tables still recorded for audit.
COPY users TO '/tmp/x';
TRUNCATE TABLE evictions, audit;
