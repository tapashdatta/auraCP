-- The CTE binding 'x' is not a table; its source 'people' is.
WITH x AS (SELECT id FROM people WHERE active) SELECT * FROM x;
