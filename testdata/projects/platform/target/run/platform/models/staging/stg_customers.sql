
    

    create  table
      "warehouse"."main"."stg_customers__dbt_tmp"
  
    
    as (
      select
    customer_id,
    first_name,
    last_name,
    signup_country
from "warehouse"."main"."raw_customers"
    );
    
  