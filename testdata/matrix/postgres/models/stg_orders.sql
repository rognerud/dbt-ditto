select
    order_id,
    customer_id,
    status,
    amount_cents
from {{ ref('raw_orders') }}
