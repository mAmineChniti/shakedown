CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, name TEXT, age INTEGER, balance REAL);
CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER, total REAL, status TEXT);
CREATE TABLE payments (id INTEGER PRIMARY KEY, order_id INTEGER REFERENCES orders(id), amount REAL);
CREATE TABLE logs (id INTEGER PRIMARY KEY, message TEXT);

WITH RECURSIVE seq(g) AS (
  SELECT 1 UNION ALL SELECT g+1 FROM seq WHERE g < 2000
)
INSERT INTO users (email, name, age, balance)
SELECT 'user' || g || '@example.com', 'User ' || g,
       CASE WHEN g % 7 = 0 THEN NULL WHEN g % 11 = 0 THEN -5 ELSE g % 120 END,
       (g % 1000) * 1.5
FROM seq;
INSERT INTO users (email, name, age) VALUES
  ('dup@example.com', 'Dup A', 30),
  ('dup@example.com', 'Dup B', 40),
  (hex(randomblob(60)) || '@example.com', 'Long', 50);

WITH RECURSIVE seq(g) AS (
  SELECT 1 UNION ALL SELECT g+1 FROM seq WHERE g < 5000
)
INSERT INTO orders (user_id, total, status)
SELECT CASE WHEN g % 97 = 0 THEN 999999 ELSE (g % 2000) + 1 END,
       g * 1.5, CASE g % 4 WHEN 0 THEN 'new' WHEN 1 THEN 'paid' WHEN 2 THEN 'shipped' ELSE 'cancelled' END
FROM seq;

INSERT INTO payments (order_id, amount) SELECT id, total FROM orders;

WITH RECURSIVE seq(g) AS (
  SELECT 1 UNION ALL SELECT g+1 FROM seq WHERE g < 5000
)
INSERT INTO logs (message) SELECT 'log ' || g FROM seq;
