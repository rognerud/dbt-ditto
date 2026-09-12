
    

    create  table
      "warehouse"."main"."region_report__dbt_tmp"
  
    
    as (
      -- Second cross-project edge, to a model documented only in the producer repo.
select
    region_code,
    region_name
from "warehouse"."main"."dim_regions"
    );
    
  