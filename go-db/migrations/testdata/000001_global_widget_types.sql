-- +goose Up
CREATE TABLE global_widget_types (
    code text PRIMARY KEY
);

-- +goose Down
DROP TABLE IF EXISTS global_widget_types;
