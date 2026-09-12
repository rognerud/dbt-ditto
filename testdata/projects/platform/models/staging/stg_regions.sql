select
    region_code,
    region_name
from {{ ref('raw_regions') }}
