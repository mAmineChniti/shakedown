ALTER TABLE payments ADD COLUMN user_id INT;
UPDATE payments SET user_id = 1;
ALTER TABLE payments ADD CONSTRAINT fk_pay_user FOREIGN KEY (user_id) REFERENCES users(id);
