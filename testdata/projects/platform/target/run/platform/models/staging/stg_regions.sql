
    

    create  table
      "warehouse"."main"."stg_regions__dbt_tmp"
  
    
    as (
      select
    region_code,
    region_name
from "warehouse"."main"."raw_regions"
    );
    
  