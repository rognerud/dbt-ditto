-- Joins two cross-project ancestors and one local one, so inheritance has to
-- span both repositories at once.
select
    c.customer_id,
    c.signup_country,
    r.region_name,
    c.lifetime_value_eur
from "warehouse"."main"."customer_report" as c
left join "warehouse"."main"."region_report" as r on c.signup_country = r.region_code