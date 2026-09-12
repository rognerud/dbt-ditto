-- Second cross-project edge, to a model documented only in the producer repo.
select
    region_code,
    region_name
from {{ ref('platform', 'dim_regions') }}
