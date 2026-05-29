-- Aliased column reads of LOAD_FILE; not a function call.
SELECT load_file_count FROM stats;
SELECT COALESCE(load_file_count, 0) FROM stats;
SELECT `LOAD_FILE` AS legacy_col FROM t;
