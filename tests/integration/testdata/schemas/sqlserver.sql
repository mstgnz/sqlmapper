CREATE TABLE customers (
    id BIGINT IDENTITY(1,1) NOT NULL,
    email NVARCHAR(255) NOT NULL,
    full_name NVARCHAR(MAX) NULL,
    is_active BIT NOT NULL CONSTRAINT DF_customers_active DEFAULT (1),
    score DECIMAL(10,2) NULL CONSTRAINT DF_customers_score DEFAULT (0),
    status NVARCHAR(20) NOT NULL CONSTRAINT DF_customers_status DEFAULT ('draft'),
    referred_by BIGINT NULL,
    created_at DATETIME2(7) NOT NULL CONSTRAINT DF_customers_created DEFAULT (sysutcdatetime()),
    CONSTRAINT PK_customers PRIMARY KEY CLUSTERED (id ASC),
    CONSTRAINT UQ_customers_email UNIQUE NONCLUSTERED (email ASC),
    CONSTRAINT CHK_customers_score CHECK (score >= 0),
    CONSTRAINT FK_customers_referrer FOREIGN KEY (referred_by) REFERENCES customers (id)
);
GO
EXEC sys.sp_addextendedproperty @name=N'MS_Description', @value=N'people who buy things',
    @level0type=N'SCHEMA', @level0name=N'dbo', @level1type=N'TABLE', @level1name=N'customers';
GO
EXEC sys.sp_addextendedproperty @name=N'MS_Description', @value=N'login address, unique',
    @level0type=N'SCHEMA', @level0name=N'dbo', @level1type=N'TABLE', @level1name=N'customers',
    @level2type=N'COLUMN', @level2name=N'email';
GO
CREATE TABLE orders (
    id BIGINT IDENTITY(1,1) NOT NULL,
    customer_id BIGINT NOT NULL,
    total DECIMAL(12,2) NOT NULL,
    placed_on DATE NOT NULL,
    note NVARCHAR(200) NULL CONSTRAINT DF_orders_note DEFAULT ('none'),
    CONSTRAINT PK_orders PRIMARY KEY CLUSTERED (id ASC),
    CONSTRAINT UQ_orders_day UNIQUE NONCLUSTERED (customer_id ASC, placed_on ASC)
);
GO
ALTER TABLE orders WITH CHECK ADD CONSTRAINT FK_orders_customer FOREIGN KEY (customer_id)
    REFERENCES customers (id) ON DELETE CASCADE ON UPDATE NO ACTION;
GO
ALTER TABLE orders WITH CHECK ADD CONSTRAINT CHK_orders_total CHECK (total >= 0);
GO
CREATE TABLE order_lines (
    order_id BIGINT NOT NULL,
    line_no INT NOT NULL,
    sku NVARCHAR(64) NOT NULL,
    qty INT NOT NULL CONSTRAINT DF_lines_qty DEFAULT (1),
    CONSTRAINT PK_order_lines PRIMARY KEY CLUSTERED (order_id ASC, line_no ASC),
    CONSTRAINT FK_lines_order FOREIGN KEY (order_id) REFERENCES orders (id) ON DELETE CASCADE
);
GO
CREATE NONCLUSTERED INDEX IX_orders_customer ON orders (customer_id ASC);
GO
CREATE UNIQUE NONCLUSTERED INDEX IX_customers_email ON customers (email ASC);
GO
CREATE NONCLUSTERED INDEX IX_lines_sku_qty ON order_lines (sku ASC, qty ASC);
GO
CREATE VIEW active_customers AS SELECT id, email, score FROM dbo.customers WHERE is_active = 1;
GO
CREATE FUNCTION order_total(@order_id BIGINT) RETURNS DECIMAL(12,2)
AS
BEGIN
    DECLARE @t DECIMAL(12,2);
    SELECT @t = COALESCE(SUM(qty), 0) FROM dbo.order_lines WHERE order_id = @order_id;
    RETURN @t;
END
GO
CREATE PROCEDURE archive_orders @cutoff DATE
AS
BEGIN
    SET NOCOUNT ON;
    DELETE FROM dbo.orders WHERE placed_on < @cutoff;
END
GO
CREATE TRIGGER customers_touch ON customers AFTER INSERT AS
BEGIN
    SET NOCOUNT ON;
    UPDATE c SET created_at = SYSUTCDATETIME() FROM dbo.customers c JOIN inserted i ON i.id = c.id;
END
GO
