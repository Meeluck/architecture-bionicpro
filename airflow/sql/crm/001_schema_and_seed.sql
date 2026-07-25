CREATE TABLE IF NOT EXISTS users (
    user_id UUID PRIMARY KEY,
    username TEXT NOT NULL,
    email TEXT NOT NULL,
    full_name TEXT NOT NULL,
    realm_role TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- These ALTER statements also make the seed applicable to an already
-- initialized development volume created by an earlier version of the task.
ALTER TABLE users ADD COLUMN IF NOT EXISTS username TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS email TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS realm_role TEXT;

CREATE TABLE IF NOT EXISTS prostheses (
    prosthesis_id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(user_id),
    serial_number TEXT NOT NULL UNIQUE,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);


CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users (username);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users (email);
CREATE INDEX IF NOT EXISTS idx_prostheses_user_id ON prostheses (user_id);
CREATE INDEX IF NOT EXISTS idx_prostheses_updated_at ON prostheses (updated_at);

-- user_id is equal to the stable Keycloak user id and therefore to the JWT
-- subject (sub). All realm users exist in CRM, while only prothetic_user
-- accounts own prostheses and have medical telemetry.
INSERT INTO users (
    user_id,
    username,
    email,
    full_name,
    realm_role,
    updated_at
)
VALUES
    (
        '6411ea7c-fde8-4fe0-9557-d283e2d23535',
        'user1',
        'user1@example.com',
        'User One',
        'user',
        CURRENT_TIMESTAMP - INTERVAL '120 days'
    ),
    (
        'e4fde989-a2c9-4b47-8352-04c2925b2630',
        'user2',
        'user2@example.com',
        'User Two',
        'user',
        CURRENT_TIMESTAMP - INTERVAL '110 days'
    ),
    (
        '79a76b0a-357d-4421-942f-f313d5964eb7',
        'admin1',
        'admin1@example.com',
        'Admin One',
        'administrator',
        CURRENT_TIMESTAMP - INTERVAL '180 days'
    ),
    (
        '44a50ba4-d96c-4c5c-b0f6-602069c63aa6',
        'prothetic1',
        'prothetic1@example.com',
        'Prothetic One',
        'prothetic_user',
        CURRENT_TIMESTAMP - INTERVAL '90 days'
    ),
    (
        'd73fbadb-e292-4d3f-baca-749b54d9debe',
        'prothetic2',
        'prothetic2@example.com',
        'Prothetic Two',
        'prothetic_user',
        CURRENT_TIMESTAMP - INTERVAL '60 days'
    ),
    (
        'c387f5e7-3fe0-44c1-9cca-99552ddb3152',
        'prothetic3',
        'prothetic3@example.com',
        'Prothetic Three',
        'prothetic_user',
        CURRENT_TIMESTAMP - INTERVAL '30 days'
    )
ON CONFLICT (user_id) DO UPDATE
SET
    username = EXCLUDED.username,
    email = EXCLUDED.email,
    full_name = EXCLUDED.full_name,
    realm_role = EXCLUDED.realm_role,
    updated_at = EXCLUDED.updated_at;

-- Remove only obsolete records from previous versions of this mock dataset.
DELETE FROM prostheses
WHERE prosthesis_id NOT IN (
    'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1',
    'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa2',
    'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa3',
    'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa4'
);

INSERT INTO prostheses (
    prosthesis_id,
    user_id,
    serial_number,
    active,
    updated_at
)
VALUES
    (
        'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1',
        '44a50ba4-d96c-4c5c-b0f6-602069c63aa6',
        'BP-HAND-001',
        TRUE,
        CURRENT_TIMESTAMP - INTERVAL '90 days'
    ),
    (
        'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa4',
        '44a50ba4-d96c-4c5c-b0f6-602069c63aa6',
        'BP-HAND-004',
        TRUE,
        CURRENT_TIMESTAMP - INTERVAL '45 days'
    ),
    (
        'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa2',
        'd73fbadb-e292-4d3f-baca-749b54d9debe',
        'BP-HAND-002',
        TRUE,
        CURRENT_TIMESTAMP - INTERVAL '60 days'
    ),
    (
        'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa3',
        'c387f5e7-3fe0-44c1-9cca-99552ddb3152',
        'BP-HAND-003',
        TRUE,
        CURRENT_TIMESTAMP - INTERVAL '30 days'
    )
ON CONFLICT (prosthesis_id) DO UPDATE
SET
    user_id = EXCLUDED.user_id,
    serial_number = EXCLUDED.serial_number,
    active = EXCLUDED.active,
    updated_at = EXCLUDED.updated_at;

-- Existing prostheses now reference the new Keycloak-aligned users, so old
-- mock users can be removed without violating the foreign key.
DELETE FROM users
WHERE user_id NOT IN (
    '6411ea7c-fde8-4fe0-9557-d283e2d23535',
    'e4fde989-a2c9-4b47-8352-04c2925b2630',
    '79a76b0a-357d-4421-942f-f313d5964eb7',
    '44a50ba4-d96c-4c5c-b0f6-602069c63aa6',
    'd73fbadb-e292-4d3f-baca-749b54d9debe',
    'c387f5e7-3fe0-44c1-9cca-99552ddb3152'
);

ALTER TABLE users ALTER COLUMN username SET NOT NULL;
ALTER TABLE users ALTER COLUMN email SET NOT NULL;
ALTER TABLE users ALTER COLUMN realm_role SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_reader') THEN
        CREATE USER crm_reader WITH PASSWORD 'crm_reader_password';
    END IF;
END
$$;

GRANT CONNECT ON DATABASE crm TO crm_reader;
GRANT USAGE ON SCHEMA public TO crm_reader;
GRANT SELECT ON users, prostheses TO crm_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO crm_reader;
