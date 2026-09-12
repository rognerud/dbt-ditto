
    

    create  table
      "warehouse"."main"."dim_customer_flat__dbt_tmp"
  
    
    as (
      -- The reverse direction: a struct unpacked back into flat columns. The
-- documentation on profile.first_name upstream belongs on first_name here.
select
    customer_id,
    profile.first_name as first_name,
    profile.last_name as last_name,
    profile.signup_country as signup_country
from "warehouse"."main"."dim_customer_profile"
    );
    
  