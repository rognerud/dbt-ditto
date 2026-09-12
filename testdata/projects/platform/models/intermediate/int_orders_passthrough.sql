-- Deliberately has no YAML file at all: an "empty middle" model that
-- documentation has to travel through to reach the marts below it.
select
    order_id,
    customer_id,
    order_date,
    status,
    amount_cents
from {{ ref('stg_orders') }}
