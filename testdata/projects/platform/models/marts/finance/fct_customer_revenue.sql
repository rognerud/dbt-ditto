select
    customer_id,
    signup_country,
    lifetime_order_count,
    lifetime_value_cents
from {{ ref('int_customer_orders') }}
