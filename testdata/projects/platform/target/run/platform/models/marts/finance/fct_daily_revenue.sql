
    

    create  table
      "warehouse"."main"."fct_daily_revenue__dbt_tmp"
  
    
    as (
      select
    order_date,
    count(*) as order_count,
    sum(amount_cents) as revenue_cents
from "warehouse"."main"."int_orders_passthrough"
where status = 'completed'
group by 1
    );
    
  