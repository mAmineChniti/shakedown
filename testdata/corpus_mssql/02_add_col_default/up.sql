ALTER TABLE users ADD verified BIT NOT NULL CONSTRAINT df_users_verified DEFAULT 0;
