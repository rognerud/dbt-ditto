select
    order_id,
    customer_id,
    order_date,
    status,
    amount_cents
from {{ ref('int_orders_passthrough') }}
