

with orders as (

    select
        customer_id,
        count(*) as lifetime_order_count,
        sum(amount_cents) as lifetime_value_cents
    from "warehouse"."main"."stg_orders"
    group by 1

)

select
    c.customer_id,
    c.first_name,
    c.last_name,
    c.signup_country,
    coalesce(o.lifetime_order_count, 0) as lifetime_order_count,
    coalesce(o.lifetime_value_cents, 0) as lifetime_value_cents
from "warehouse"."main"."stg_customers" as c
left join orders as o on c.customer_id = o.customer_id