CREATE TABLE users (id INT IDENTITY(1,1) PRIMARY KEY, email NVARCHAR(255) NOT NULL, name NVARCHAR(255), age INT, balance DECIMAL(10,2));
CREATE TABLE orders (id INT IDENTITY(1,1) PRIMARY KEY, user_id INT, total DECIMAL(12,2), status NVARCHAR(20));
CREATE TABLE payments (id INT IDENTITY(1,1) PRIMARY KEY, order_id INT, amount DECIMAL(12,2), CONSTRAINT fk_pay_order FOREIGN KEY (order_id) REFERENCES orders(id));
CREATE TABLE logs (id INT IDENTITY(1,1) PRIMARY KEY, message NVARCHAR(MAX));

;WITH seq AS (
  SELECT 1 g UNION ALL SELECT g+1 FROM seq WHERE g < 2000
)
INSERT INTO users (email, name, age, balance)
SELECT 'user' + CAST(g AS NVARCHAR) + '@example.com', 'User ' + CAST(g AS NVARCHAR),
       CASE WHEN g % 7 = 0 THEN NULL WHEN g % 11 = 0 THEN -5 ELSE g % 120 END,
       (g % 1000) * 1.5
FROM seq OPTION (MAXRECURSION 10000);
INSERT INTO users (email, name, age) VALUES
  ('dup@example.com', 'Dup A', 30),
  ('dup@example.com', 'Dup B', 40),
  (REPLACE(REPLICATE('x',120),'x','x') + '@example.com', 'Long', 50);

;WITH seq AS (
  SELECT 1 g UNION ALL SELECT g+1 FROM seq WHERE g < 5000
)
INSERT INTO orders (user_id, total, status)
SELECT CASE WHEN g % 97 = 0 THEN 999999 ELSE (g % 2000) + 1 END,
       g * 1.5, CASE g % 4 WHEN 0 THEN 'new' WHEN 1 THEN 'paid' WHEN 2 THEN 'shipped' ELSE 'cancelled' END
FROM seq OPTION (MAXRECURSION 10000);

INSERT INTO payments (order_id, amount) SELECT id, total FROM orders;

;WITH seq AS (
  SELECT 1 g UNION ALL SELECT g+1 FROM seq WHERE g < 5000
)
INSERT INTO logs (message) SELECT 'log ' + CAST(g AS NVARCHAR) FROM seq OPTION (MAXRECURSION 10000);
