-- name: ListOrders :many
SELECT
    order_id,
    total_amount,
    placed_at
FROM orders
WHERE customer_id = chgen.arg('CustomerID')
ORDER BY placed_at DESC
LIMIT chgen.arg('Limit')
