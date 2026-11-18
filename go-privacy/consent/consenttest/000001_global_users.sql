-- +goose Up
-- Stands in for the app's own global_users, which consent events reference.
-- Only the test schema uses this file (it is not an app migration); an app already has the table.
CREATE TABLE global_users (
    id uuid PRIMARY KEY,
    email text NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS global_users;
