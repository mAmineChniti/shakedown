CREATE TABLE users (id serial PRIMARY KEY, email text NOT NULL, name text, age int, balance numeric(10,2));
CREATE TABLE orders (id serial PRIMARY KEY, user_id int, total numeric, status text);
CREATE TABLE payments (id serial PRIMARY KEY, order_id int REFERENCES orders(id), amount numeric);
CREATE TABLE logs (id serial PRIMARY KEY, message text);

INSERT INTO users (email, name, age, balance)
SELECT 'user'||g||'@example.com', 'User '||g,
       CASE WHEN g % 7 = 0 THEN NULL WHEN g % 11 = 0 THEN -5 ELSE g % 120 END,
       (g % 1000) * 1.5
FROM generate_series(1, 2000) g;
INSERT INTO users (email, name, age) VALUES
  ('dup@example.com', 'Dup A', 30),
  ('dup@example.com', 'Dup B', 40),
  (repeat('x', 50)||'@example.com', 'Long', 50);

INSERT INTO orders (user_id, total, status)
SELECT CASE WHEN g % 97 = 0 THEN 999999 ELSE (g % 2000) + 1 END,
       g * 1.5, (ARRAY['new','paid','shipped','cancelled'])[1 + g % 4]
FROM generate_series(1, 5000) g;

INSERT INTO payments (order_id, amount)
SELECT id, total FROM orders;

INSERT INTO logs (message)
SELECT 'log ' || g FROM generate_series(1, 5000) g;
