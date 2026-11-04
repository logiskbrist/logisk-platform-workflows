-- Platform-admin bootstrap, never dispatched by the app workflow.
-- Supply expected_database, migration_role and runtime_role with psql -v.
-- Supply the new password through MIGRATION_PASSWORD, never a CLI argument.
\set ON_ERROR_STOP on
\getenv migration_password MIGRATION_PASSWORD
SELECT current_database() = :'expected_database'
   AND :'migration_role' ~ '^[a-z][a-z0-9_]{0,62}$'
   AND :'migration_role' <> :'runtime_role'
   AND length(:'migration_password') >= 24
   AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'runtime_role') AS safe \gset
\if :safe
\else
  \echo 'Refusing: check database, role names, runtime role and password length.'
  \quit 1
\endif

BEGIN;
SELECT format('CREATE ROLE %I LOGIN', :'migration_role')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'migration_role') \gexec
-- Azure's administrator is not a superuser. Refuse privileged existing roles
-- rather than attempting superuser-only attribute changes, even to false.
SELECT NOT (rolsuper OR rolreplication OR rolbypassrls) AS unprivileged
FROM pg_roles WHERE rolname=:'migration_role' \gset
\if :unprivileged
\else
  \echo 'Refusing: migration role has privileged attributes.'
  ROLLBACK;
  \quit 1
\endif
ALTER ROLE :"migration_role" LOGIN INHERIT NOCREATEDB NOCREATEROLE PASSWORD :'migration_password';
SELECT NOT EXISTS (
  SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member
  WHERE r.rolname=:'migration_role'
) AS isolated \gset
\if :isolated
\else
  \echo 'Refusing: migration role already inherits another role.'
  ROLLBACK;
  \quit 1
\endif

-- The runtime currently owns its objects and performs startup schema fixes.
-- Keep those owner privileges through membership; never grant the runtime's
-- server-admin role to the migration login (the reverse grant is unsafe).
GRANT :"migration_role" TO :"runtime_role" WITH INHERIT TRUE, SET TRUE;
ALTER DATABASE :"expected_database" OWNER TO :"migration_role";
ALTER SCHEMA public OWNER TO :"migration_role";

-- REASSIGN OWNED also touches shared objects, so enumerate only this app's
-- public objects. Leave extension members and objects owned by others alone.
SELECT format('ALTER %s %I.%I OWNER TO %I',
  CASE c.relkind WHEN 'S' THEN 'SEQUENCE' WHEN 'v' THEN 'VIEW'
    WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'f' THEN 'FOREIGN TABLE' ELSE 'TABLE' END,
  n.nspname,c.relname,:'migration_role')
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_roles r ON r.oid=c.relowner
WHERE n.nspname='public' AND r.rolname=:'runtime_role' AND c.relkind IN ('r','p','S','v','m','f')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e')
ORDER BY c.relkind='S',c.oid \gexec

SELECT format('ALTER %s %I.%I(%s) OWNER TO %I',
  CASE p.prokind WHEN 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END,
  n.nspname,p.proname,pg_get_function_identity_arguments(p.oid),:'migration_role')
FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_roles r ON r.oid=p.proowner
WHERE n.nspname='public' AND r.rolname=:'runtime_role' AND p.prokind IN ('f','p','w')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e') \gexec

SELECT format('ALTER TYPE %I.%I OWNER TO %I',n.nspname,t.typname,:'migration_role')
FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace JOIN pg_roles r ON r.oid=t.typowner
WHERE n.nspname='public' AND r.rolname=:'runtime_role' AND t.typtype IN ('e','d')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_type'::regclass AND d.objid=t.oid AND d.deptype='e') \gexec
COMMIT;
