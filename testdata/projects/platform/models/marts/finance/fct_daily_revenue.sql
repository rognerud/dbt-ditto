select
    order_date,
    count(*) as order_count,
    sum(amount_cents) as revenue_cents
from {{ ref('int_orders_passthrough') }}
where status = 'completed'
group by 1
