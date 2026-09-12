
    

    create  table
      "warehouse"."main"."fct_customer_revenue__dbt_tmp"
  
    
    as (
      select
    customer_id,
    signup_country,
    lifetime_order_count,
    lifetime_value_cents
from "warehouse"."main"."int_customer_orders"
    );
    
  