
    

    create  table
      "warehouse"."main"."stg_orders__dbt_tmp"
  
    
    as (
      select
    order_id,
    customer_id,
    order_date,
    status,
    amount_cents
from "warehouse"."main"."raw_orders"
    );
    
  