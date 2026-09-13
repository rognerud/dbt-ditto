-- Flat upstream columns packed into a struct. The subfields carry the same
-- meaning as the columns they came from, so their documentation should follow
-- them in: profile.first_name is stg_customers.first_name.
select
    customer_id,
    struct_pack(
        first_name := first_name,
        last_name := last_name,
        signup_country := signup_country
    ) as profile
from {{ ref('stg_customers') }}
