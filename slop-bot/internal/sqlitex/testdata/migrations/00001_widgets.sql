-- +goose Up
CREATE TABLE widgets (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);
CREATE TABLE parts (
  id INTEGER PRIMARY KEY,
  widget_id INTEGER NOT NULL REFERENCES widgets (id)
);

-- +goose Down
DROP TABLE parts;
DROP TABLE widgets;
