select
    customer_id,
    signup_country,
    lifetime_order_count,
    lifetime_value_cents,
    lifetime_value_cents / 100.0 as lifetime_value_eur
from "warehouse"."main"."dim_customers"