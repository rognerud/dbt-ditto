select
    order_id,
    customer_id,
    order_date,
    status,
    amount_cents
from {{ source('crm', 'raw_orders') }}
