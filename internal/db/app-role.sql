-- Runtime database role for the FHIR server. It is deliberately NOSUPERUSER and
-- NOBYPASSRLS so the tenant_isolation policies in schema.sql are enforced. It is
-- granted CREATE so the server can apply the embedded schema on first start; the
-- objects it creates are owned by this role, and FORCE ROW LEVEL SECURITY applies
-- the policies to the owner too.
--
-- Run as an init script (docker-entrypoint-initdb.d) by the bootstrap role.
CREATE ROLE fhir_app WITH LOGIN PASSWORD 'fhir_app' NOSUPERUSER NOBYPASSRLS NOCREATEROLE;
GRANT CREATE, CONNECT, TEMPORARY ON DATABASE fhirdb TO fhir_app;
GRANT ALL ON SCHEMA public TO fhir_app;
