ALTER TABLE orders ADD COLUMN note text DEFAULT md5(random()::text);
