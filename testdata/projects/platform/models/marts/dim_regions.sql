{{ config(access='public') }}

select
    region_code,
    region_name
from {{ ref('stg_regions') }}
