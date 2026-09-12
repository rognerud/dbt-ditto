-- Diamond: both legs lead back to raw_customers and raw_orders.
select
    c.customer_id,
    c.first_name,
    c.last_name,
    c.signup_country,
    count(o.order_id) as lifetime_order_count,
    coalesce(sum(o.amount_cents), 0) as lifetime_value_cents
from {{ ref('stg_customers') }} as c
left join {{ ref('int_orders_passthrough') }} as o on c.customer_id = o.customer_id
group by 1, 2, 3, 4
