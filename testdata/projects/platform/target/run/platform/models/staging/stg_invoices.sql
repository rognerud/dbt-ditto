
    

    create  table
      "warehouse"."main"."stg_invoices__dbt_tmp"
  
    
    as (
      select
    invoice_id,
    order_id,
    invoice_total_cents,
    issued_on
from "warehouse"."main"."raw_invoices"
    );
    
  