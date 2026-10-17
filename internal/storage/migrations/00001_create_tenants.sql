-- +goose Up
CREATE TABLE tenants (
    id         uuid PRIMARY KEY,
    name       text NOT NULL CHECK (length(btrim(name)) > 0),
    plan       text NOT NULL CHECK (plan IN ('free', 'pro')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE tenants;
