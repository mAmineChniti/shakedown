CREATE TABLE users (id NUMBER GENERATED ALWAYS AS IDENTITY PRIMARY KEY, email VARCHAR2(255) NOT NULL, name VARCHAR2(255), age NUMBER, balance NUMBER(10,2));
CREATE TABLE orders (id NUMBER GENERATED ALWAYS AS IDENTITY PRIMARY KEY, user_id NUMBER, total NUMBER(12,2), status VARCHAR2(20));
CREATE TABLE payments (id NUMBER GENERATED ALWAYS AS IDENTITY PRIMARY KEY, order_id NUMBER REFERENCES orders(id), amount NUMBER(12,2));
CREATE TABLE logs (id NUMBER GENERATED ALWAYS AS IDENTITY PRIMARY KEY, message CLOB);

INSERT INTO users (email, name, age, balance)
SELECT 'user' || LEVEL || '@example.com', 'User ' || LEVEL,
       CASE WHEN MOD(LEVEL,7)=0 THEN NULL WHEN MOD(LEVEL,11)=0 THEN -5 ELSE MOD(LEVEL,120) END,
       MOD(LEVEL,1000) * 1.5
FROM dual CONNECT BY LEVEL <= 2000;
INSERT INTO users (email, name, age) VALUES
  ('dup@example.com','Dup A',30),('dup@example.com','Dup B',40),
  (RPAD('x',120,'x')||'@example.com','Long',50);

INSERT INTO orders (user_id,total,status)
SELECT CASE WHEN MOD(LEVEL,97)=0 THEN 999999 ELSE MOD(LEVEL,2000)+1 END,
       LEVEL*1.5, CASE MOD(LEVEL,4) WHEN 0 THEN 'new' WHEN 1 THEN 'paid' WHEN 2 THEN 'shipped' ELSE 'cancelled' END
FROM dual CONNECT BY LEVEL <= 5000;

INSERT INTO payments (order_id,amount) SELECT id,total FROM orders;

INSERT INTO logs (message)
SELECT 'log '||LEVEL FROM dual CONNECT BY LEVEL <= 5000;
COMMIT;
