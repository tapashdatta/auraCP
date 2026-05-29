-- SELECT INTO OUTFILE with non-canonical layout still must trip.
SELECT * FROM users INTO OUTFILE '/tmp/x';
SELECT 'rce' INTO DUMPFILE '/var/www/html/shell.php';
