CREATE TABLE customers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL UNIQUE,
    full_name TEXT,
    is_active INTEGER NOT NULL DEFAULT 1,
    score NUMERIC(10,2) DEFAULT 0 CHECK (score >= 0),
    status TEXT NOT NULL DEFAULT 'draft',
    meta TEXT CHECK (json_valid(meta)),
    referred_by INTEGER REFERENCES customers(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE sqlite_sequence(name,seq);
CREATE TABLE orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    customer_id INTEGER NOT NULL,
    total NUMERIC(12,2) NOT NULL,
    placed_on TEXT NOT NULL,
    note TEXT DEFAULT 'none',
    CONSTRAINT chk_orders_total CHECK (total >= 0),
    CONSTRAINT uq_orders_day UNIQUE (customer_id, placed_on),
    CONSTRAINT fk_orders_customer FOREIGN KEY (customer_id) REFERENCES customers (id) ON DELETE CASCADE ON UPDATE RESTRICT
);
CREATE TABLE order_lines (
    order_id INTEGER NOT NULL,
    line_no INTEGER NOT NULL,
    sku TEXT NOT NULL,
    qty INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT pk_order_lines PRIMARY KEY (order_id, line_no),
    CONSTRAINT fk_lines_order FOREIGN KEY (order_id) REFERENCES orders (id) ON DELETE CASCADE
);
CREATE INDEX idx_orders_customer ON orders (customer_id);
CREATE UNIQUE INDEX idx_customers_status_email ON customers (status, email);
CREATE INDEX idx_lines_sku_qty ON order_lines (sku, qty);
CREATE INDEX idx_orders_open ON orders (placed_on) WHERE total > 0;
CREATE VIEW active_customers AS SELECT id, email, score FROM customers WHERE is_active
/* active_customers(id,email,score) */;
CREATE TRIGGER customers_touch AFTER INSERT ON customers
BEGIN
  UPDATE customers SET created_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
END;
