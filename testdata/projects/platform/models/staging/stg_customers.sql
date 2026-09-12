select
    customer_id,
    first_name,
    last_name,
    signup_country
from {{ ref('raw_customers') }}
