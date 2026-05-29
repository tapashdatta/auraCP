-- pg_read_file as a column name must NOT trigger the forbidden matcher.
SELECT "pg_read_file" AS function_name FROM doc_examples;
-- column literally named pg_read_file_count.
SELECT pg_read_file_count FROM stats;
