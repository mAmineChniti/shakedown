ALTER TABLE users ADD COLUMN verified INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_users_verified ON users (verified);
