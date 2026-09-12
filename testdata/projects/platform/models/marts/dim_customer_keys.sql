-- Upstream spells these lower case; inheritance has to match case-insensitively.
select
    customer_id as "CUSTOMER_ID",
    signup_country as "Signup_Country"
from {{ ref('stg_customers') }}
