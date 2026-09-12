select
    order_id,
    customer_id,
    order_date,
    status,
    amount_cents
from "warehouse"."main"."int_orders_passthrough"