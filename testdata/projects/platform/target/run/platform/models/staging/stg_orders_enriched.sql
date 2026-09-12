
    

    create  table
      "warehouse"."main"."stg_orders_enriched__dbt_tmp"
  
    
    as (
      -- Two upstreams document `status` and `order_id` differently: the seed-backed
-- stg_orders and the CRM source. Inheritance has to pick one deterministically.
select
    o.order_id,
    o.customer_id,
    o.order_date,
    o.status,
    o.amount_cents,
    s.amount_cents as source_amount_cents
from "warehouse"."main"."stg_orders" as o
join "warehouse"."main"."stg_source_orders" as s on o.order_id = s.order_id
    );
    
  