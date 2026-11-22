-- +goose Up
INSERT INTO dbtest_aux.settings (key, value) VALUES ('seeded', 'yes');

-- +goose Down
DELETE FROM dbtest_aux.settings WHERE key = 'seeded';
