CREATE TABLE orders
(
    order_id     String,
    customer_id  String,
    total_amount Decimal(18, 2),
    placed_at    DateTime64(3)
)
ENGINE = MergeTree
ORDER BY (customer_id, order_id);
