-- Aggregates. Each output column is the same quantity as the input column it
-- was computed from, so the documentation should carry across the aggregation.
select
    customer_id,
    sum(amount_cents) as total_amount_cents,
    max(order_date) as max_order_date,
    count(order_id) as order_id_count
from {{ ref('stg_orders') }}
group by 1
