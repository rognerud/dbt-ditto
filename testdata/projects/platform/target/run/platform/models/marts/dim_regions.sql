
    

    create  table
      "warehouse"."main"."dim_regions__dbt_tmp"
  
    
    as (
      

select
    region_code,
    region_name
from "warehouse"."main"."stg_regions"
    );
    
  