select
    customer_id,
    count(*) as order_count,
    sum(amount_cents) as amount_cents
from {{ ref('stg_orders') }}
group by 1
